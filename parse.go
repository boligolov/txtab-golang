package txtab

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// SupportedVersions lists the texttables= versions this parser implements.
var SupportedVersions = []string{"0.1"}

var (
	reKeyword  = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	reName     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	reUint     = regexp.MustCompile(`^\d+$`)
	rePosInt   = regexp.MustCompile(`^[1-9]\d*$`)
	reSQLToken = regexp.MustCompile(`^[a-z][a-z0-9_]*(?:-[0-9a-z][0-9a-z.]*)?$`)
	reScheme   = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9+.-]*):`)
)

var (
	fileKeys    = set("texttables", "tables")
	defaultKeys = set("delim", "quote", "null", "encoding")
	tableKeys   = set("name", "rows", "header", "columns", "reference", "storage", "section-size", "data-size")
	columnAttrs = set("name", "title", "description", "index", "type", "format", "example", "unit", "required", "default",
		"min", "max", "len-min", "len-max", "enum", "pattern", "regexp-dialect", "unique", "references", "role", "agg",
		"order", "separator", "link", "formula", "materialized")
	numericTypes = set("int", "long", "float", "double", "decimal")
	aggNames     = set("count-nonnull", "count-null", "count-distinct", "sum", "avg", "min", "max", "len-min", "len-max")
	numericAggs  = set("sum", "avg", "min", "max")
	stringAggs   = set("len-min", "len-max")
	channels     = set("x", "y", "x2", "y2", "color", "size", "theta", "radius", "shape", "opacity", "column", "row", "detail", "order", "tooltip", "text")
	modifiers    = set("title", "aggregate", "bin", "stack", "sort", "limit", "hole")
	safeSchemes  = set("http", "https", "mailto")
)

var chartTypes = map[string]func(c map[string]bool) bool{
	"bar":       func(c map[string]bool) bool { return c["x"] && c["y"] },
	"line":      func(c map[string]bool) bool { return c["x"] && c["y"] },
	"area":      func(c map[string]bool) bool { return c["x"] && c["y"] },
	"point":     func(c map[string]bool) bool { return c["x"] || c["y"] },
	"circle":    func(c map[string]bool) bool { return c["x"] && c["y"] },
	"arc":       func(c map[string]bool) bool { return c["theta"] },
	"rect":      func(c map[string]bool) bool { return c["x"] && c["y"] && c["color"] },
	"tick":      func(c map[string]bool) bool { return c["x"] || c["y"] },
	"boxplot":   func(c map[string]bool) bool { return c["x"] || c["y"] },
	"text":      func(c map[string]bool) bool { return c["x"] && c["y"] && c["text"] },
	"sparkline": func(c map[string]bool) bool { return c["y"] },
}

// SQLDialects maps dialect tokens and aliases to canonical names (sql.md#dialects).
var SQLDialects = map[string]string{
	"ansi": "ansi", "mysql": "mysql", "mariadb": "mariadb", "postgres": "postgres", "postgresql": "postgres",
	"pg": "postgres", "mssql": "mssql", "sqlserver": "mssql", "sqlite": "sqlite", "oracle": "oracle", "db2": "db2",
	"clickhouse": "clickhouse", "snowflake": "snowflake", "bigquery": "bigquery", "duckdb": "duckdb",
}

func set(xs ...string) map[string]bool {
	m := make(map[string]bool, len(xs))
	for _, x := range xs {
		m[x] = true
	}
	return m
}

func itoa(n int) string { return strconv.Itoa(n) }

// SplitDialect splits a dialect token by the longest well-known dialect prefix.
func SplitDialect(token string) (dialect, version string, known bool) {
	names := make([]string, 0, len(SQLDialects))
	for n := range SQLDialects {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool {
		return len(names[i]) > len(names[j]) || (len(names[i]) == len(names[j]) && names[i] < names[j])
	})
	for _, n := range names {
		if token == n {
			return SQLDialects[n], "", true
		}
		if strings.HasPrefix(token, n+"-") {
			return SQLDialects[n], token[len(n)+1:], true
		}
	}
	return token, "", false
}

// ---------------------------------------------------------------- helpers

// ReferenceEscapes: reference= is absolute or leaves its directory.
func ReferenceEscapes(ref string) bool {
	if strings.HasPrefix(ref, "/") || strings.HasPrefix(ref, `\`) || regexp.MustCompile(`^[A-Za-z]:`).MatchString(ref) || strings.Contains(ref, "://") {
		return true
	}
	depth := 0
	for _, seg := range regexp.MustCompile(`[/\\]`).Split(ref, -1) {
		if seg == ".." {
			depth--
		} else if seg != "" && seg != "." {
			depth++
		}
		if depth < 0 {
			return true
		}
	}
	return false
}

// SafeURL: the scheme is http, https or mailto.
func SafeURL(u string) bool {
	m := reScheme.FindStringSubmatch(u)
	return m != nil && safeSchemes[strings.ToLower(m[1])]
}

func percentEncode(v string) string {
	var b strings.Builder
	for _, c := range []byte(v) {
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '.' || c == '_' || c == '~' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// TemplatePiece is a literal or a placeholder of a link= template.
type TemplatePiece struct {
	Lit, Ph string
	IsPh    bool
}

// ParseTemplate parses a link= template; ok is false when a `{` is never closed.
func ParseTemplate(tpl string) (pieces []TemplatePiece, ok bool) {
	var lit strings.Builder
	for i := 0; i < len(tpl); i++ {
		c := tpl[i]
		if c == '{' {
			if i+1 < len(tpl) && tpl[i+1] == '{' {
				lit.WriteByte('{')
				i++
				continue
			}
			end := strings.IndexByte(tpl[i+1:], '}')
			if end < 0 {
				return nil, false
			}
			if lit.Len() > 0 {
				pieces = append(pieces, TemplatePiece{Lit: lit.String()})
				lit.Reset()
			}
			pieces = append(pieces, TemplatePiece{Ph: tpl[i+1 : i+1+end], IsPh: true})
			i += end + 1
			continue
		}
		lit.WriteByte(c)
	}
	if lit.Len() > 0 {
		pieces = append(pieces, TemplatePiece{Lit: lit.String()})
	}
	return pieces, true
}

// StoredCell: position of a stored or materialized column by name, else of a header cell; -1 if none.
func StoredCell(t *Table, name string) int {
	for _, c := range t.Columns {
		if c.Name == name && c.Physical() && c.Cell >= 0 {
			return c.Cell
		}
	}
	if t.Data != nil {
		for i, h := range t.Data.Header {
			if h == name {
				return i
			}
		}
	}
	return -1
}

func placeholderCell(t *Table, ph string, cell int) int {
	if ph == "$" {
		return cell
	}
	if reUint.MatchString(ph) {
		if t.Header == 0 {
			n, _ := strconv.Atoi(ph)
			return n
		}
		return -1
	}
	return StoredCell(t, ph)
}

// ExpandTemplate returns the final URL for one record; ok is false when a placeholder is null or unknown.
func ExpandTemplate(t *Table, pieces []TemplatePiece, record []string, cell int) (string, bool) {
	single := len(pieces) == 1 && pieces[0].IsPh
	var b strings.Builder
	for _, p := range pieces {
		if !p.IsPh {
			b.WriteString(p.Lit)
			continue
		}
		pos := placeholderCell(t, p.Ph, cell)
		if pos < 0 || pos >= len(record) || t.IsNull(record[pos]) {
			return "", false
		}
		if single {
			b.WriteString(record[pos])
		} else {
			b.WriteString(percentEncode(record[pos]))
		}
	}
	return b.String(), true
}

// IDCell is the id column: the single role=id column, else the single unique=1 column; -1 if none.
func IDCell(t *Table) int {
	for _, pick := range []func(*Column) bool{
		func(c *Column) bool { return c.Attrs["role"] == "id" },
		func(c *Column) bool { return c.Attrs["unique"] == "1" },
	} {
		var found []*Column
		for _, c := range t.Columns {
			if pick(c) {
				found = append(found, c)
			}
		}
		if len(found) == 1 {
			return found[0].Cell
		}
		if len(found) > 1 {
			return -1
		}
	}
	return -1
}

func resolveCol(t *Table, col string) int {
	if t.Header == 0 {
		if !reUint.MatchString(col) {
			return -1
		}
		n, _ := strconv.Atoi(col)
		if t.Width >= 0 && n < t.Width {
			return n
		}
		return -1
	}
	return StoredCell(t, col)
}

func resolveRow(t *Table, row, key *string) int {
	var recs [][]string
	if t.Data != nil {
		recs = t.Data.Records
	}
	if row != nil {
		if !reUint.MatchString(*row) {
			return -1
		}
		n, _ := strconv.Atoi(*row)
		if n < len(recs) {
			return n
		}
		return -1
	}
	if key != nil {
		id := IDCell(t)
		if id < 0 {
			return -1
		}
		for i, r := range recs {
			if id < len(r) && !t.IsNull(r[id]) && r[id] == *key {
				return i
			}
		}
	}
	return -1
}

func boolPtr(b bool) *bool { return &b }

// ResolveAddresses resolves note and link addresses against the table's data and returns the warnings.
func ResolveAddresses(t *Table) []Diagnostic {
	var out []Diagnostic
	for _, n := range t.Notes {
		wantsRow := n.Row != nil || n.Key != nil
		r, c := -1, -1
		if wantsRow {
			r = resolveRow(t, n.Row, n.Key)
		}
		if n.Col != nil {
			c = resolveCol(t, *n.Col)
		}
		n.ResolvedRow, n.ResolvedCol = r, c
		ok := (!wantsRow || r >= 0) && (n.Col == nil || c >= 0)
		n.Resolved = boolPtr(ok)
		if !ok {
			out = append(out, newDiag("note-unresolved", "note address does not resolve", n.Line, t.Index))
		}
	}
	seen := map[[2]int]bool{}
	for _, l := range t.Links {
		r, c := resolveRow(t, l.Row, l.Key), resolveCol(t, l.Col)
		l.ResolvedRow, l.ResolvedCol = r, c
		ok := r >= 0 && c >= 0
		l.Resolved = boolPtr(ok)
		if !ok {
			out = append(out, newDiag("link-unresolved", "link address does not resolve", l.Line, t.Index))
			continue
		}
		if seen[[2]int{r, c}] {
			out = append(out, newDiag("link-duplicate", "two links on one cell; the last wins", l.Line, t.Index))
		}
		seen[[2]int{r, c}] = true
	}
	return out
}

// DataChecks runs the checks that need the data: defaults against nulls, link templates producing unsafe URLs.
func DataChecks(t *Table) []Diagnostic {
	var out []Diagnostic
	var recs [][]string
	if t.Data != nil {
		recs = t.Data.Records
	}
	for _, c := range t.Columns {
		if c.Cell < 0 {
			continue
		}
		if _, has := c.Attrs["default"]; has && !c.Computed {
			for _, r := range recs {
				if c.Cell < len(r) && t.IsNull(r[c.Cell]) {
					out = append(out, newDiag("default-with-nulls", "column "+c.label()+" has default= and nulls in the data", c.Line, t.Index))
					break
				}
			}
		}
		if tpl, has := c.Attrs["link"]; has {
			pieces, ok := ParseTemplate(tpl)
			if !ok {
				continue
			}
			all := true
			for _, p := range pieces {
				if p.IsPh && placeholderCell(t, p.Ph, c.Cell) < 0 {
					all = false
				}
			}
			if !all {
				continue
			}
			for _, r := range recs {
				if u, ok := ExpandTemplate(t, pieces, r, c.Cell); ok && !SafeURL(u) {
					out = append(out, newDiag("link-unsafe-scheme", "column "+c.label()+": a link's scheme is not http, https or mailto", c.Line, t.Index))
					break
				}
			}
		}
	}
	for _, l := range t.Links {
		if l.Resolved != nil && *l.Resolved && !SafeURL(l.Href) {
			out = append(out, newDiag("link-unsafe-scheme", "link scheme is not http, https or mailto", l.Line, t.Index))
		}
	}
	return out
}

// VerifyOK: the parse succeeded and no warning escalates (errors.md, "→ FAIL").
func VerifyOK(doc *Doc, extra ...Diagnostic) bool {
	if !doc.OK {
		return false
	}
	for _, d := range append(append([]Diagnostic{}, doc.Diagnostics...), extra...) {
		if d.Severity == Warn && Codes[d.Code].Verify == "fail" {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------- parse

func splitLines(b []byte) [][]byte {
	parts := bytes.Split(b, []byte{'\n'})
	if len(parts) > 0 && len(parts[len(parts)-1]) == 0 {
		parts = parts[:len(parts)-1]
	}
	for i, p := range parts {
		parts[i] = bytes.TrimSuffix(p, []byte{'\r'})
	}
	return parts
}

// Parse parses a .txtab file. It never panics on bad input: see Doc.OK, Doc.Error, Doc.Diagnostics.
func Parse(data []byte) (doc *Doc) {
	doc = &Doc{FirstLine: map[string]string{}, Meta: map[string]string{}}
	defer func() {
		if r := recover(); r != nil {
			pf, ok := r.(parseFail)
			if !ok {
				panic(r)
			}
			doc.OK = false
			d := pf.d
			doc.Error = &d
			doc.Diagnostics = append(doc.Diagnostics, d)
		}
	}()
	p := &parser{data: data, doc: doc}
	if p.run() {
		doc.OK = true
	}
	return doc
}

type parser struct {
	data         []byte
	doc          *Doc
	lines        [][]byte // UTF-8 for wide files (decoded), else raw bytes
	wide         bool
	P            string
	fl           map[string]string
	fileDelim    string
	fileQuote    string
	fileEncoding string
	fileDecode   Decoder
	tablesDecl   int
	names        map[string]bool
	seenMeta     map[string]bool
}

func (p *parser) warn(code string, line, table int, format string, a ...any) {
	p.doc.Diagnostics = append(p.doc.Diagnostics, newDiag(code, fmt.Sprintf(format, a...), line, table))
}

func (p *parser) isSys(i int) bool {
	l := p.lines[i]
	return string(l) == p.P || bytes.HasPrefix(l, []byte(p.P+" "))
}

func (p *parser) text(i int, dec Decoder) string {
	if p.wide {
		return string(p.lines[i])
	}
	if dec == nil {
		dec = p.fileDecode
	}
	s, err := dec(p.lines[i])
	if err != nil {
		failf("encoding-invalid-bytes", i+1, -1, "line %d does not decode", i+1)
	}
	return s
}

func (p *parser) splitSys(s string) (string, string) {
	if s == p.P {
		return "--", ""
	}
	rest := s[len(p.P)+1:]
	if k := strings.IndexByte(rest, ' '); k >= 0 {
		return rest[:k], rest[k+1:]
	}
	return rest, ""
}

func (p *parser) kv(payload, code string, line, table int, warnDupes bool) map[string]string {
	pairs, err := tokenizeKV(payload)
	if err != "" {
		failf(code, line, table, "%s", err)
	}
	attrs := map[string]string{}
	for _, kv := range pairs {
		if _, dup := attrs[kv.Key]; dup && warnDupes {
			p.warn("key-duplicate", line, table, "%s= repeated; the last wins", kv.Key)
		}
		attrs[kv.Key] = kv.Value
	}
	return attrs
}

func (p *parser) run() bool {
	doc := p.doc
	enc, bomLen := SniffBOM(p.data)
	doc.BOM = enc
	var first string
	if enc != "" && !isASCIICompatible(enc) {
		text, err := DecoderFor(enc)(p.data[bomLen:])
		if err != nil {
			if !p.looksRecognized(enc, bomLen) {
				return false
			}
			failf("encoding-invalid-bytes", 0, -1, "bytes do not decode as %s", enc)
		}
		p.wide = true
		p.lines = splitLines([]byte(text))
		if len(p.lines) > 0 {
			first = string(p.lines[0])
		}
	} else {
		p.lines = splitLines(p.data[bomLen:])
		if len(p.lines) > 0 {
			first = latin1(p.lines[0])
		}
	}
	sp := strings.IndexByte(first, ' ')
	if sp < 0 || !strings.HasPrefix(first[sp+1:], "texttables=") {
		return false
	}
	doc.Recognized = true
	for _, r := range first {
		if r > 0x7f {
			failf("first-line-not-ascii", 1, -1, "line 1 must be ASCII")
		}
	}
	P := first[:sp]
	p.P = P
	doc.Prefix = P
	bad := len(P) < 1 || len(P) > 8
	for i := 0; i < len(P); i++ {
		if P[i] < 0x21 || P[i] > 0x7e {
			bad = true
		}
	}
	if c := P[0]; (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
		bad = true
	}
	if bad {
		failf("prefix-invalid", 1, -1, "prefix %q must be 1-8 printable ASCII characters, not starting with a letter or digit", P)
	}

	fl := p.kv(first[sp+1:], "kv-malformed", 1, -1, true)
	p.fl = fl
	doc.FirstLine = fl
	doc.Version = fl["texttables"]
	supported := false
	for _, v := range SupportedVersions {
		supported = supported || v == fl["texttables"]
	}
	if !supported {
		p.warn("version-unknown", 1, -1, "texttables=%s is not implemented; reading best-effort", fl["texttables"])
	}
	for _, k := range sortedKeys(fl) {
		if fileKeys[k] || defaultKeys[k] {
			continue
		}
		if tableKeys[k] {
			p.warn("key-misplaced", 1, -1, "%s= is a table key; ignored on the first line", k)
		} else if !strings.HasPrefix(k, "x-") {
			p.warn("key-unknown", 1, -1, "unknown key %s=", k)
		}
	}
	p.tablesDecl = -1
	if v, ok := fl["tables"]; ok {
		if !reUint.MatchString(v) {
			failf("value-invalid", 1, -1, "tables=%s", v)
		}
		p.tablesDecl, _ = strconv.Atoi(v)
	}
	p.checkDialectValues(fl, 1, -1)
	p.fileDelim = orDefault(fl, "delim", "comma")
	p.fileQuote = orDefault(fl, "quote", "none")
	p.checkPrefix(p.fileDelim, p.fileQuote, 1, -1)

	p.fileEncoding = "UTF-8"
	if enc != "" {
		p.fileEncoding = enc
		if v, ok := fl["encoding"]; ok && !sameEncoding(v, enc) {
			p.warn("encoding-mismatch", 1, -1, "encoding=%s but the BOM says %s; the BOM wins", v, enc)
		}
	} else if v, ok := fl["encoding"]; ok {
		if !isASCIICompatible(v) || DecoderFor(v) == nil {
			p.warn("encoding-unknown", 1, -1, "unknown encoding %s; using UTF-8", v)
		} else {
			p.fileEncoding = v
		}
	}
	doc.Encoding = p.fileEncoding
	p.fileDecode = DecoderFor(p.fileEncoding)
	doc.LineCount = len(p.lines)
	doc.SystemLines = append(doc.SystemLines, SystemLine{1, first, "texttables", -1})

	var cur *Table
	p.names = map[string]bool{}
	p.seenMeta = map[string]bool{}
	idx := func(t *Table) int {
		if t == nil {
			return -1
		}
		return t.Index
	}
	n := len(p.lines)
	for i := 1; i < n; {
		if p.isSys(i) {
			text := p.text(i, nil)
			kw, payload := p.splitSys(text)
			ln := i + 1
			if kw == "--" {
				doc.SystemLines = append(doc.SystemLines, SystemLine{ln, text, "--", idx(cur)})
				i++
				continue
			}
			if !reKeyword.MatchString(kw) {
				failf("keyword-malformed", ln, idx(cur), "%q is not a keyword", kw)
			}
			if kw == "table" {
				if cur != nil {
					p.finishTable(cur)
				}
				cur = p.startTable(payload, ln, len(doc.Tables))
				doc.Tables = append(doc.Tables, cur)
				p.seenMeta = map[string]bool{}
				doc.SystemLines = append(doc.SystemLines, SystemLine{ln, text, kw, cur.Index})
				i++
				continue
			}
			doc.SystemLines = append(doc.SystemLines, SystemLine{ln, text, kw, idx(cur)})
			p.directive(kw, payload, ln, cur)
			i++
			continue
		}
		if cur == nil {
			failf("directive-after-data", i+1, -1, "data before the first table line")
		}
		if cur.Reference != "" {
			failf("data-in-reference-table", i+1, cur.Index, "table %s has reference= and no inline data", cur.Label())
		}
		i = p.readData(cur, i)
		p.finishTable(cur)
		cur = nil
	}
	if cur != nil {
		p.finishTable(cur)
	}

	if len(doc.Tables) > 1 && p.tablesDecl < 0 {
		failf("tables-missing", 1, -1, "%d table lines and no tables= on the first line", len(doc.Tables))
	}
	expected := 1
	if p.tablesDecl >= 0 {
		expected = p.tablesDecl
	}
	if len(doc.Tables) != expected {
		failf("tables-mismatch", 1, -1, "tables=%d but %d table lines", expected, len(doc.Tables))
	}
	for _, t := range doc.Tables {
		for _, c := range t.Columns {
			ref, has := c.Attrs["references"]
			if !has {
				continue
			}
			ok := false
			if dot := strings.IndexByte(ref, '.'); dot >= 0 {
				tn, col := ref[:dot], ref[dot+1:]
				for _, x := range doc.Tables {
					if x.Name == "" || x.Name != tn {
						continue
					}
					for _, xc := range x.Columns {
						ok = ok || xc.Name == col
					}
					if x.Data != nil {
						for _, h := range x.Data.Header {
							ok = ok || h == col
						}
					}
					break
				}
			}
			if !ok {
				p.warn("references-unresolved", c.Line, t.Index, "references=%s does not resolve", ref)
			}
		}
	}
	return true
}

func orDefault(m map[string]string, k, def string) string {
	if v, ok := m[k]; ok {
		return v
	}
	return def
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func (p *parser) looksRecognized(enc string, bomLen int) bool {
	step := 2
	if strings.HasPrefix(enc, "UTF-32") {
		step = 4
	}
	le := strings.HasSuffix(enc, "LE")
	var s []byte
	for i := bomLen; i+step <= len(p.data) && len(s) < 64; i += step {
		b := p.data[i+step-1]
		if le {
			b = p.data[i]
		}
		if b == '\n' {
			break
		}
		s = append(s, b)
	}
	str := string(s)
	sp := strings.IndexByte(str, ' ')
	return sp > 0 && strings.HasPrefix(str[sp+1:], "texttables=")
}

func (p *parser) checkDialectValues(kv map[string]string, line, table int) {
	if v, ok := kv["delim"]; ok && v == "" {
		failf("value-invalid", line, table, "delim= is empty")
	}
	if v, ok := kv["quote"]; ok && v == "" {
		failf("value-invalid", line, table, "quote= is empty")
	}
}

func (p *parser) checkPrefix(delim, quote string, line, table int) {
	dc, qc := DelimChars(delim), QuoteChar(quote)
	if (dc != "" && strings.Contains(p.P, dc)) || (qc != "" && strings.Contains(p.P, qc)) {
		failf("prefix-invalid", line, table, "prefix %q contains the delimiter or the quote character", p.P)
	}
}

func (p *parser) startTable(payload string, ln, index int) *Table {
	kv := p.kv(payload, "kv-malformed", ln, index, true)
	for _, k := range sortedKeys(kv) {
		if tableKeys[k] || defaultKeys[k] {
			continue
		}
		if fileKeys[k] {
			p.warn("key-misplaced", ln, index, "%s= is a file key; ignored on a table line", k)
		} else if !strings.HasPrefix(k, "x-") {
			p.warn("key-unknown", ln, index, "unknown key %s=", k)
		}
	}
	rowsV, ok := kv["rows"]
	if !ok {
		failf("table-missing-rows", ln, index, "table line without rows=")
	}
	if !reUint.MatchString(rowsV) {
		failf("value-invalid", ln, index, "rows=%s", rowsV)
	}
	if h, ok := kv["header"]; ok && h != "0" && h != "1" {
		failf("value-invalid", ln, index, "header=%s", h)
	}
	for _, k := range []string{"columns", "data-size"} {
		if v, ok := kv[k]; ok && !reUint.MatchString(v) {
			failf("value-invalid", ln, index, "%s=%s", k, v)
		}
	}
	if v, ok := kv["section-size"]; ok && !rePosInt.MatchString(v) {
		failf("value-invalid", ln, index, "section-size=%s", v)
	}
	storage := orDefault(kv, "storage", "rows")
	if storage != "rows" && storage != "columns" {
		failf("value-invalid", ln, index, "storage=%s", storage)
	}
	p.checkDialectValues(kv, ln, index)
	if name, ok := kv["name"]; ok {
		if !reName.MatchString(name) {
			failf("table-name-invalid", ln, index, "table name %q", name)
		}
		if p.names[name] {
			failf("table-name-duplicate", ln, index, "two tables named %s", name)
		}
		p.names[name] = true
	}
	reference := kv["reference"]
	if reference != "" && ReferenceEscapes(reference) {
		failf("reference-escapes-dir", ln, index, "reference=%s", reference)
	}
	if storage == "columns" && !strings.HasSuffix(reference, "/") {
		failf("storage-columns-without-dir", ln, index, "storage=columns needs reference= to a directory")
	}
	_, hasSection := kv["section-size"]
	if hasSection && storage != "columns" {
		p.warn("section-size-without-columns", ln, index, "section-size= ignored without storage=columns")
	}
	delim := orDefault(kv, "delim", p.fileDelim)
	quote := orDefault(kv, "quote", p.fileQuote)
	p.checkPrefix(delim, quote, ln, index)
	encoding := p.fileEncoding
	if v, ok := kv["encoding"]; ok {
		known := DecoderFor(v) != nil
		if !known {
			p.warn("encoding-unknown", ln, index, "unknown encoding %s; using UTF-8", v)
		} else {
			encoding = v
		}
		if known && reference == "" && !sameEncoding(encoding, p.fileEncoding) && (!isASCIICompatible(encoding) || !isASCIICompatible(p.fileEncoding)) {
			failf("encoding-mixed-inline", ln, index, "inline table in %s inside a %s file", encoding, p.fileEncoding)
		}
	}
	null := orDefault(kv, "null", p.fl["null"])
	t := &Table{
		Index: index, Line: ln, Keys: kv, Name: kv["name"], Header: 1, Delim: delim, Quote: quote, NullMarker: null,
		Encoding: encoding, Reference: reference, Storage: storage, DataSize: -1, ColumnsDeclared: -1,
		Meta: map[string]string{}, Aggregates: map[string][]string{}, RowsRead: -1, Width: -1,
	}
	t.Rows, _ = strconv.Atoi(rowsV)
	if kv["header"] == "0" {
		t.Header = 0
	}
	if hasSection && storage == "columns" {
		t.SectionSize, _ = strconv.Atoi(kv["section-size"])
	}
	if v, ok := kv["data-size"]; ok {
		t.DataSize, _ = strconv.Atoi(v)
	}
	if v, ok := kv["columns"]; ok {
		t.ColumnsDeclared, _ = strconv.Atoi(v)
	}
	if reference == "" {
		t.RowsRead = 0
	}
	return t
}

func strPtr(m map[string]string, k string) *string {
	if v, ok := m[k]; ok {
		return &v
	}
	return nil
}

func (p *parser) directive(kw, payload string, ln int, t *Table) {
	doc := p.doc
	ti := -1
	if t != nil {
		ti = t.Index
	}
	switch {
	case strings.HasPrefix(kw, "meta-"):
		key := kw[5:]
		if p.seenMeta[key] {
			p.warn("meta-duplicate", ln, ti, "meta-%s repeated; the last wins", key)
		}
		p.seenMeta[key] = true
		if t != nil {
			t.Meta[key] = strings.TrimSpace(payload)
		} else {
			doc.Meta[key] = strings.TrimSpace(payload)
		}
	case kw == "sql-pre-data" || kw == "sql-post-data" || kw == "sql-dql":
		token, statement := payload, ""
		if k := strings.IndexByte(payload, ' '); k >= 0 {
			token, statement = payload[:k], payload[k+1:]
		}
		if token == "" || !reSQLToken.MatchString(token) {
			failf("sql-dialect-missing", ln, ti, "%s needs a dialect token first", kw)
		}
		dialect, version, known := SplitDialect(token)
		if !known {
			p.warn("sql-unknown-dialect", ln, ti, "unknown SQL dialect %s", token)
		}
		reference := ""
		// sql.md#external-scripts-reference: a single reference=<path> token instead of a statement
		if strings.HasPrefix(statement, "reference=") {
			pairs, err := tokenizeKV(statement)
			if err == "" && len(pairs) != 1 {
				err = "reference= must be the only token after the dialect"
			}
			if err != "" {
				failf("kv-malformed", ln, ti, "%s", err)
			}
			reference = pairs[0].Value
			if reference == "" || ReferenceEscapes(reference) {
				failf("reference-escapes-dir", ln, ti, "reference=%s", reference)
			}
			statement = ""
		}
		line := &SQLLine{ln, kw[4:], token, dialect, version, known, statement, reference, ti}
		doc.SQL = append(doc.SQL, line)
		if t != nil {
			t.SQL = append(t.SQL, line)
		}
	case kw == "note":
		a := p.kv(payload, "note-malformed", ln, ti, false)
		if _, ok := a["text"]; !ok {
			failf("note-missing-text", ln, ti, "note without text=")
		}
		_, hasRow := a["row"]
		_, hasKey := a["key"]
		_, hasCol := a["col"]
		if hasRow && hasKey {
			failf("note-row-and-key", ln, ti, "note with both row= and key=")
		}
		if t == nil && (hasCol || hasRow || hasKey) {
			failf("note-address-in-file-block", ln, -1, "an addressed note before the first table")
		}
		rowish := hasRow || hasKey
		target := "table"
		switch {
		case t == nil:
			target = "file"
		case rowish && hasCol:
			target = "cell"
		case hasCol:
			target = "column"
		case rowish:
			target = "row"
		}
		note := &Note{Line: ln, Attrs: a, Text: a["text"], Col: strPtr(a, "col"), Row: strPtr(a, "row"), Key: strPtr(a, "key"), Target: target, ResolvedRow: -1, ResolvedCol: -1}
		if t != nil {
			t.Notes = append(t.Notes, note)
		} else {
			doc.Notes = append(doc.Notes, note)
		}
	case kw == "column":
		if t == nil {
			failf("column-in-file-block", ln, -1, "column before the first table line")
		}
		t.Columns = p.addColumn(t, payload, ln)
	case strings.HasPrefix(kw, "agg-"):
		if t == nil {
			failf("agg-in-file-block", ln, -1, "%s before the first table line", kw)
		}
		name := kw[4:]
		if !aggNames[name] {
			p.warn("agg-unknown", ln, ti, "unknown aggregate %s", kw)
			return
		}
		if _, dup := t.Aggregates[name]; dup {
			p.warn("agg-duplicate", ln, ti, "%s repeated; the last wins", kw)
		}
		vals, err := SplitRecord(payload, DelimChars(t.Delim), QuoteChar(t.Quote))
		if err != nil {
			vals = []string{payload}
		}
		t.Aggregates[name] = vals
	case kw == "link":
		if t == nil {
			failf("link-in-file-block", ln, -1, "link before the first table line")
		}
		a := p.kv(payload, "link-malformed", ln, ti, false)
		_, hasHref := a["href"]
		_, hasRow := a["row"]
		_, hasKey := a["key"]
		_, hasCol := a["col"]
		if !hasHref {
			failf("link-missing-href", ln, ti, "link without href=")
		}
		if hasRow && hasKey {
			failf("link-row-and-key", ln, ti, "link with both row= and key=")
		}
		if !hasCol || (!hasRow && !hasKey) {
			failf("link-missing-address", ln, ti, "link needs col= and row= or key=")
		}
		t.Links = append(t.Links, &Link{Line: ln, Attrs: a, Href: a["href"], Col: a["col"], Row: strPtr(a, "row"), Key: strPtr(a, "key"), ResolvedRow: -1, ResolvedCol: -1})
	case kw == "chart" || strings.HasPrefix(kw, "chart-"):
		if t == nil {
			failf("chart-in-file-block", ln, -1, "%s before the first table line", kw)
		}
		t.Charts = append(t.Charts, p.chart(kw, payload, ln, t))
	default:
		p.warn("keyword-unknown", ln, ti, "unknown keyword %s", kw)
	}
}

func (p *parser) addColumn(t *Table, payload string, ln int) []*Column {
	a := p.kv(payload, "column-malformed-attribute", ln, t.Index, true)
	for _, k := range sortedKeys(a) {
		if !columnAttrs[k] && !strings.HasPrefix(k, "x-") {
			p.warn("column-unknown-attribute", ln, t.Index, "unknown column attribute %s=", k)
		}
	}
	name, hasName := a["name"]
	if hasName && !reName.MatchString(name) {
		failf("column-name-invalid", ln, t.Index, "column name %q", name)
	}
	index := -1
	if v, ok := a["index"]; ok {
		if !reUint.MatchString(v) {
			failf("column-malformed-attribute", ln, t.Index, "index=%s", v)
		}
		index, _ = strconv.Atoi(v)
	}
	if v, ok := a["materialized"]; ok && v != "0" && v != "1" {
		failf("column-malformed-attribute", ln, t.Index, "materialized=%s", v)
	}
	formula, computed := a["formula"]
	if computed && index >= 0 {
		failf("formula-index-forbidden", ln, t.Index, "a computed column has no index=")
	}
	if computed && t.Header == 0 {
		failf("formula-requires-header", ln, t.Index, "computed columns need header=1")
	}
	if t.Header == 1 && !hasName {
		failf("column-missing-name", ln, t.Index, "header=1 and the column has no name=")
	}
	if t.Header == 0 && index < 0 && !computed {
		failf("column-missing-index", ln, t.Index, "header=0 and the column has no index=")
	}
	_, hasDefault := a["default"]
	_, hasRequired := a["required"]
	if computed && (hasDefault || hasRequired) {
		p.warn("computed-default-ignored", ln, t.Index, "default= and required= are ignored on a computed column")
	}
	title, hasTitle := a["title"]
	col := &Column{Line: ln, Attrs: a, Name: name, Title: title, HasTitle: hasTitle, Index: index, Type: a["type"],
		Formula: formula, Computed: computed, Materialized: a["materialized"] == "1", Cell: -1}
	cols := t.Columns
	for k, x := range cols {
		if (hasName && x.Name == name) || (t.Header == 0 && index >= 0 && x.Index == index) {
			p.warn("column-duplicate", ln, t.Index, "two column lines for %s; the last wins", col.label())
			cols = append(cols[:k:k], cols[k+1:]...)
			break
		}
	}
	return append(cols, col)
}

func (p *parser) chart(kw, payload string, ln int, t *Table) *Chart {
	if kw != "chart" {
		engine := kw[6:]
		if engine == "vega" {
			if !json.Valid([]byte(payload)) {
				failf("chart-vega-invalid-json", ln, t.Index, "chart-vega payload is not JSON")
			}
			return &Chart{Line: ln, Engine: engine, Attrs: map[string]string{}, Payload: payload, Known: true}
		}
		p.warn("chart-unknown-type", ln, t.Index, "unknown chart engine %s", engine)
		return &Chart{Line: ln, Engine: engine, Attrs: map[string]string{}, Payload: payload}
	}
	a := p.kv(payload, "kv-malformed", ln, t.Index, false)
	typ, hasType := a["type"]
	if !hasType {
		failf("chart-missing-type", ln, t.Index, "chart without type=")
	}
	name, hasName := a["name"]
	if !hasName {
		failf("chart-missing-name", ln, t.Index, "chart without name=")
	}
	_, known := chartTypes[typ]
	if !known {
		p.warn("chart-unknown-type", ln, t.Index, "unknown chart type %s", typ)
	}
	for _, c := range t.Charts {
		if c.Name == name {
			p.warn("chart-duplicate-name", ln, t.Index, "two charts named %s", name)
			break
		}
	}
	return &Chart{Line: ln, Attrs: a, Type: typ, Name: name, Known: known}
}

func (p *parser) checkCharts(t *Table) {
	for _, ch := range t.Charts {
		if ch.Engine != "" || !ch.Known {
			continue
		}
		used := map[string]bool{}
		for _, k := range sortedKeys(ch.Attrs) {
			v := ch.Attrs[k]
			if k == "type" || k == "name" || strings.HasPrefix(k, "x-") || modifiers[k] {
				continue
			}
			if !channels[k] {
				p.warn("chart-unknown-channel", ch.Line, t.Index, "unknown chart channel %s=", k)
				continue
			}
			used[k] = true
			cols := []string{v}
			if k == "tooltip" {
				cols = strings.Split(v, ",")
			}
			for _, col := range cols {
				if col == "count()" {
					continue
				}
				found := false
				for _, c := range t.Columns {
					found = found || c.Name == col
				}
				if !found {
					failf("chart-unknown-column", ch.Line, t.Index, "chart %s: %s=%s has no column line", ch.Name, k, col)
				}
			}
		}
		if !chartTypes[ch.Type](used) {
			failf("chart-missing-required-channel", ch.Line, t.Index, "chart %s: type=%s lacks a required channel", ch.Name, ch.Type)
		}
	}
}

func (p *parser) readData(t *Table, start int) int {
	dec := p.fileDecode
	if t.Encoding != p.fileEncoding {
		if d := DecoderFor(t.Encoding); d != nil {
			dec = d
		}
	}
	delim, quote := DelimChars(t.Delim), QuoteChar(t.Quote)
	n := len(p.lines)
	split := func(i int) []string {
		f, err := SplitRecord(p.text(i, dec), delim, quote)
		if err != nil {
			failf("quoted-value-raw-newline", i+1, t.Index, "a quoted value runs past the end of the line")
		}
		return f
	}
	isTableLine := func(i int) bool {
		if !p.isSys(i) {
			return false
		}
		kw, _ := p.splitSys(p.text(i, nil))
		return kw == "table"
	}
	i := start
	t.DataLine = start + 1
	var header []string
	if t.Header == 1 {
		header = split(i)
		i++
	}
	var records [][]string
	var lineOf []int
	for len(records) < t.Rows && i < n {
		if isTableLine(i) {
			break
		}
		if p.isSys(i) {
			p.warn("record-starts-with-prefix", i+1, t.Index, "record on line %d starts with the prefix", i+1)
		}
		records = append(records, split(i))
		lineOf = append(lineOf, i+1)
		i++
	}
	extra := false
	for i < n && !isTableLine(i) {
		if p.isSys(i) {
			text := p.text(i, nil)
			kw, _ := p.splitSys(text)
			if kw != "--" {
				failf("directive-after-data", i+1, t.Index, "%s after the data of table %s", kw, t.Label())
			}
			p.doc.SystemLines = append(p.doc.SystemLines, SystemLine{i + 1, text, "--", t.Index})
		} else {
			records = append(records, split(i))
			lineOf = append(lineOf, i+1)
			extra = true
		}
		i++
	}
	if len(records) != t.Rows {
		more := ""
		if extra {
			more = " (read on to the next table)"
		}
		p.warn("rows-mismatch", t.Line, t.Index, "rows=%d but %d records%s", t.Rows, len(records), more)
	}
	width := -1
	if header != nil {
		width = len(header)
	} else if len(records) > 0 {
		width = len(records[0])
	}
	t.Width = width
	if width >= 0 {
		for k, r := range records {
			if len(r) == width {
				continue
			}
			if quote == "" && len(r) > width {
				failf("quote-none-delimiter-in-value", lineOf[k], t.Index, "quote=none and a value contains the delimiter")
			}
			failf("record-arity-mismatch", lineOf[k], t.Index, "record has %d fields, the table has %d", len(r), width)
		}
	}
	t.Data = &TableData{Header: header, Records: records}
	t.RowsRead = len(records)
	return i
}

func indexOf(xs []string, v string) int {
	for i, x := range xs {
		if x == v {
			return i
		}
	}
	return -1
}

func (p *parser) finishTable(t *Table) {
	if t.Reference == "" && t.Data == nil && t.Rows > 0 {
		p.warn("rows-mismatch", t.Line, t.Index, "rows=%d but the table has no data", t.Rows)
	}
	var header []string
	if t.Data != nil {
		header = t.Data.Header
	}
	var physical []*Column
	for _, c := range t.Columns {
		if c.Physical() {
			physical = append(physical, c)
		}
	}
	if t.Reference == "" {
		if t.Header == 1 && header != nil {
			for _, c := range t.Columns {
				want := c.Name
				if c.HasTitle {
					want = c.Title
				}
				if c.Computed && !c.Materialized {
					if indexOf(header, c.Name) >= 0 {
						failf("computed-materialized-mismatch", c.Line, t.Index, "computed column %s has data but no materialized=1", c.Name)
					}
					continue
				}
				pos := indexOf(header, want)
				if pos < 0 {
					if c.Computed {
						failf("computed-materialized-mismatch", c.Line, t.Index, "materialized=1 but column %s has no data", c.Name)
					}
					if c.HasTitle {
						p.warn("column-title-header-mismatch", c.Line, t.Index, "no header cell equals title %q", c.Title)
					} else {
						p.warn("column-name-header-mismatch", c.Line, t.Index, "no header cell equals name %s", c.Name)
					}
				} else {
					c.Cell = pos
				}
			}
		} else if t.Header == 0 {
			over := false
			for _, c := range physical {
				if c.Index < 0 {
					continue
				}
				if t.Width >= 0 && c.Index >= t.Width {
					over = true
				} else {
					c.Cell = c.Index
				}
			}
			if over || (t.Width >= 0 && len(physical) > t.Width) {
				p.warn("column-count-mismatch", t.Line, t.Index, "the column lines do not fit the %d fields of the data", t.Width)
			}
		}
		if t.ColumnsDeclared >= 0 && t.Width >= 0 && t.ColumnsDeclared != t.Width {
			p.warn("columns-mismatch", t.Line, t.Index, "columns=%d but the data has %d", t.ColumnsDeclared, t.Width)
		}
	} else if t.Header == 0 {
		for _, c := range physical {
			c.Cell = c.Index
		}
	}

	declared, stored, computedNames := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, c := range t.Columns {
		if c.Name != "" {
			declared[c.Name] = true
		}
		if c.Computed {
			computedNames[c.Name] = true
		}
	}
	for _, c := range physical {
		if !c.Computed {
			stored[c.Name] = true
		}
	}
	for _, h := range header {
		stored[h] = true
	}
	for _, c := range t.Columns {
		if unit := c.Attrs["unit"]; strings.HasPrefix(unit, "@") && !declared[unit[1:]] {
			failf("unit-unknown-column", c.Line, t.Index, "unit=%s names no column of the table", unit)
		}
		if !c.Computed {
			continue
		}
		refs, err := ParseFormula(c.Formula)
		if err != nil {
			failf("formula-parse-error", c.Line, t.Index, "formula %q: %v", c.Formula, err)
		}
		for _, r := range refs {
			if computedNames[r] {
				failf("formula-references-computed", c.Line, t.Index, "formula of %s uses computed column %s", c.Name, r)
			}
			if !stored[r] {
				failf("formula-unknown-reference", c.Line, t.Index, "formula of %s uses unknown column %s", c.Name, r)
			}
		}
	}

	for _, c := range t.Columns {
		tpl, has := c.Attrs["link"]
		if !has {
			continue
		}
		pieces, ok := ParseTemplate(tpl)
		if !ok {
			p.warn("link-template-malformed", c.Line, t.Index, "link= template of %s has an unterminated {", c.label())
			continue
		}
		for _, pc := range pieces {
			if !pc.IsPh || pc.Ph == "$" {
				continue
			}
			var known bool
			if reUint.MatchString(pc.Ph) {
				known = t.Header == 0
			} else {
				known = indexOf(header, pc.Ph) >= 0
				for _, x := range physical {
					known = known || x.Name == pc.Ph
				}
			}
			if !known {
				p.warn("link-unknown-column", c.Line, t.Index, "link= placeholder {%s} names no stored column", pc.Ph)
				break
			}
		}
	}

	slots := t.Width
	if slots < 0 {
		slots = t.ColumnsDeclared
	}
	for _, name := range sortedAggKeys(t.Aggregates) {
		values := t.Aggregates[name]
		if slots >= 0 && len(values) > slots {
			p.warn("agg-arity-mismatch", 0, t.Index, "agg-%s has %d values for %d columns", name, len(values), slots)
		}
		for pos, v := range values {
			if v == "" {
				continue
			}
			var col *Column
			for _, x := range t.Columns {
				if x.Cell == pos {
					col = x
					break
				}
			}
			if col == nil || col.Type == "" {
				continue
			}
			if (numericAggs[name] && !numericTypes[col.Type]) || (stringAggs[name] && col.Type != "string") {
				p.warn("agg-type-mismatch", 0, t.Index, "agg-%s on %s column %s", name, col.Type, col.label())
			}
		}
	}

	p.checkCharts(t)
	if t.Data != nil {
		p.doc.Diagnostics = append(p.doc.Diagnostics, ResolveAddresses(t)...)
		p.doc.Diagnostics = append(p.doc.Diagnostics, DataChecks(t)...)
	}
}

func sortedAggKeys(m map[string][]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
