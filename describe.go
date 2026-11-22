package txtab

import (
	"fmt"
	"math/big"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	describeDelims = []string{",", "\t", ";", "|"}
	delimToName    = map[string]string{",": "comma", "\t": "tab", ";": "semicolon", "|": "pipe"}
	reInt          = regexp.MustCompile(`^-?\d+$`)
	reDec          = regexp.MustCompile(`^-?\d+\.\d+$`)
	reFloat        = regexp.MustCompile(`^-?(?:\d+\.?\d*|\.\d+)(?:[eE][+-]?\d+)$`)
	reDate         = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	reDatetime     = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}(?::\d{2}(?:\.\d+)?)?(?:Z|[+-]\d{2}:?\d{2})?$`)
	reBool         = regexp.MustCompile(`(?i)^(?:true|false)$`)
	reNonName      = regexp.MustCompile(`[^A-Za-z0-9_]+`)
)

func guessDelimiter(lines []string) string {
	best, bestScore := ",", -1
	for _, d := range describeDelims {
		var counts []int
		for i, l := range lines {
			if i >= 20 {
				break
			}
			f, err := SplitRecord(l, d, `"`)
			if err != nil {
				counts = append(counts, 0)
			} else {
				counts = append(counts, len(f))
			}
		}
		if len(counts) == 0 || counts[0] < 2 {
			continue
		}
		same := 0
		for _, c := range counts {
			if c == counts[0] {
				same++
			}
		}
		if score := same*100 + counts[0]; score > bestScore {
			best, bestScore = d, score
		}
	}
	return best
}

func all(vs []string, f func(string) bool) bool {
	for _, v := range vs {
		if !f(v) {
			return false
		}
	}
	return true
}

// InferType guesses a column type from its values (nulls ignored).
func InferType(values []string) string {
	var vs []string
	for _, v := range values {
		if v != "" {
			vs = append(vs, v)
		}
	}
	switch {
	case len(vs) == 0:
		return "string"
	case all(vs, reInt.MatchString):
		if all(vs, func(v string) bool {
			n, err := strconv.ParseInt(v, 10, 64)
			return err == nil && n >= -2147483648 && n <= 2147483647
		}) {
			return "int"
		}
		return "long"
	case all(vs, func(v string) bool { return reInt.MatchString(v) || reDec.MatchString(v) }):
		return "decimal"
	case all(vs, func(v string) bool { return reInt.MatchString(v) || reDec.MatchString(v) || reFloat.MatchString(v) }):
		return "double"
	case all(vs, reBool.MatchString):
		return "boolean"
	case all(vs, reDate.MatchString):
		return "date"
	case all(vs, reDatetime.MatchString):
		return "datetime"
	}
	return "string"
}

func decimalSum(vs []string) string {
	sum := new(big.Rat)
	scale := 0
	for _, v := range vs {
		if k := strings.IndexByte(v, '.'); k >= 0 && len(v)-k-1 > scale {
			scale = len(v) - k - 1
		}
		r, _ := new(big.Rat).SetString(v)
		sum.Add(sum, r)
	}
	return sum.FloatString(scale)
}

func columnName(h string, k int, used map[string]bool) string {
	var b strings.Builder
	for _, r := range h {
		if r < 0x80 {
			b.WriteRune(r)
		} else if unicode.IsSpace(r) {
			b.WriteByte(' ')
		}
	}
	n := strings.ToLower(strings.Trim(reNonName.ReplaceAllString(b.String(), "_"), "_"))
	if n == "" || !((n[0] >= 'a' && n[0] <= 'z') || n[0] == '_') {
		if n == "" {
			n = "col_" + itoa(k)
		} else {
			n = "col_" + n
		}
	}
	out := n
	for i := 2; used[out]; i++ {
		out = fmt.Sprintf("%s_%d", n, i)
	}
	used[out] = true
	return out
}

func quoteIfNeeded(v, delim string) string {
	if strings.Contains(v, delim) || strings.Contains(v, `"`) || v != strings.TrimSpace(v) {
		return `"` + strings.ReplaceAll(v, `"`, `""`) + `"`
	}
	return v
}

func kvValue(v string) string {
	if v == "" || strings.ContainsAny(v, " \t\"") {
		return `"` + strings.ReplaceAll(v, `"`, `""`) + `"`
	}
	return v
}

// DescribeCSV describes a plain CSV/TSV text as Text Tables: inferred types, aggregates, the data inline.
func DescribeCSV(text, fileName string) (string, error) {
	lines := fileLines(strings.TrimPrefix(text, "\ufeff"))
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return "", fmt.Errorf("the file is empty")
	}
	delim := guessDelimiter(lines)
	var rows [][]string
	for i, l := range lines {
		f, err := SplitRecord(l, delim, `"`)
		if err != nil {
			return "", fmt.Errorf("line %d: a quoted value continues on the next line; Text Tables records are single-line", i+1)
		}
		rows = append(rows, f)
	}
	header, rows := rows[0], rows[1:]
	width := len(header)
	for i, r := range rows {
		if len(r) != width {
			return "", fmt.Errorf("line %d has %d fields, the header has %d", i+2, len(r), width)
		}
	}
	type col struct {
		name, title, typ string
		values           []string
	}
	used := map[string]bool{}
	cols := make([]col, width)
	for k, h := range header {
		vals := make([]string, len(rows))
		for i, r := range rows {
			vals[i] = r[k]
		}
		name := columnName(h, k, used)
		title := ""
		if h != name {
			title = h
		}
		cols[k] = col{name, title, InferType(vals), vals}
	}
	needsQuote := false
	var raw []string
	for _, r := range append([][]string{header}, rows...) {
		for _, v := range r {
			needsQuote = needsQuote || strings.Contains(v, delim) || strings.Contains(v, `"`)
		}
		raw = append(raw, strings.Join(r, delim))
	}
	P := "#"
	for _, cand := range []string{"#", "##", "%%", "//", ";;"} {
		ok := !strings.Contains(cand, delim)
		for _, l := range raw {
			ok = ok && l != cand && !strings.HasPrefix(l, cand+" ")
		}
		if ok {
			P = cand
			break
		}
	}
	base := fileName
	if k := strings.LastIndex(base, "."); k > 0 {
		base = base[:k]
	}
	table := strings.Trim(reNonName.ReplaceAllString(base, "_"), "_")
	if table == "" {
		table = "data"
	}
	if table[0] >= '0' && table[0] <= '9' {
		table = "_" + table
	}
	if !reName.MatchString(table) {
		table = "data"
	}
	var out []string
	q := ""
	if needsQuote {
		q = " quote=double"
	}
	out = append(out,
		fmt.Sprintf("%s texttables=0.1 delim=%s%s", P, delimToName[delim], q),
		fmt.Sprintf("%s meta-source %s", P, fileName),
		fmt.Sprintf("%s meta-tool txtab-golang", P),
		fmt.Sprintf("%s table name=%s rows=%d columns=%d", P, table, len(rows), width))
	for _, c := range cols {
		t := ""
		if c.title != "" {
			t = " title=" + kvValue(c.title)
		}
		out = append(out, fmt.Sprintf("%s column name=%s%s type=%s", P, c.name, t, c.typ))
	}
	nonNull := func(c col) []string {
		var vs []string
		for _, v := range c.values {
			if v != "" {
				vs = append(vs, v)
			}
		}
		return vs
	}
	agg := func(name string, f func(c col) string) {
		vals := make([]string, len(cols))
		any := false
		for i, c := range cols {
			vals[i] = quoteIfNeeded(f(c), delim)
			any = any || vals[i] != ""
		}
		if any {
			out = append(out, fmt.Sprintf("%s agg-%s %s", P, name, strings.Join(vals, delim)))
		}
	}
	exact := set("int", "long", "decimal")
	numeric := set("int", "long", "decimal", "double")
	byFloat := func(vs []string) []string {
		s := append([]string{}, vs...)
		sort.SliceStable(s, func(i, j int) bool {
			a, _ := strconv.ParseFloat(s[i], 64)
			b, _ := strconv.ParseFloat(s[j], 64)
			return a < b
		})
		return s
	}
	agg("count-nonnull", func(c col) string { return itoa(len(nonNull(c))) })
	agg("count-null", func(c col) string { return itoa(len(c.values) - len(nonNull(c))) })
	agg("count-distinct", func(c col) string {
		seen := map[string]bool{}
		for _, v := range nonNull(c) {
			seen[v] = true
		}
		return itoa(len(seen))
	})
	agg("sum", func(c col) string {
		if !exact[c.typ] || len(nonNull(c)) == 0 {
			return ""
		}
		return decimalSum(nonNull(c))
	})
	agg("min", func(c col) string {
		if !numeric[c.typ] || len(nonNull(c)) == 0 {
			return ""
		}
		return byFloat(nonNull(c))[0]
	})
	agg("max", func(c col) string {
		if !numeric[c.typ] || len(nonNull(c)) == 0 {
			return ""
		}
		s := byFloat(nonNull(c))
		return s[len(s)-1]
	})
	lenAgg := func(min bool) func(c col) string {
		return func(c col) string {
			vs := nonNull(c)
			if c.typ != "string" || len(vs) == 0 {
				return ""
			}
			best := utf8.RuneCountInString(vs[0])
			for _, v := range vs[1:] {
				n := utf8.RuneCountInString(v)
				if (min && n < best) || (!min && n > best) {
					best = n
				}
			}
			return itoa(best)
		}
	}
	agg("len-min", lenAgg(true))
	agg("len-max", lenAgg(false))
	for _, r := range append([][]string{header}, rows...) {
		vals := make([]string, len(r))
		for i, v := range r {
			if needsQuote {
				vals[i] = quoteIfNeeded(v, delim)
			} else {
				vals[i] = v
			}
		}
		out = append(out, strings.Join(vals, delim))
	}
	return strings.Join(out, "\n") + "\n", nil
}
