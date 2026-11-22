package txtab

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Source is where external data comes from: a folder, an archive.
type Source interface {
	// Read returns a file by path relative to the main file's directory; ok is false when it does not exist.
	Read(path string) (data []byte, ok bool, err error)
	// List returns the names directly inside a directory (directories end with "/"); ok is false when it does not exist.
	List(dir string) (names []string, ok bool, err error)
}

// DirSource reads external data next to a .txtab on the file system.
type DirSource struct{ Base string }

func (s DirSource) Read(path string) ([]byte, bool, error) {
	p := filepath.Join(s.Base, filepath.FromSlash(path))
	st, err := os.Stat(p)
	if err != nil || st.IsDir() {
		return nil, false, nil
	}
	b, err := os.ReadFile(p)
	return b, err == nil, err
}

func (s DirSource) List(dir string) ([]string, bool, error) {
	entries, err := os.ReadDir(filepath.Join(s.Base, filepath.FromSlash(dir)))
	if err != nil {
		return nil, false, nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name()+"/")
		} else {
			out = append(out, e.Name())
		}
	}
	return out, true, nil
}

// GapError is a spec-allowed feature this implementation lacks (e.g. a compression method).
type GapError struct{ What string }

func (e *GapError) Error() string { return e.What }

// OpenResult is the outcome of opening a table's external data.
type OpenResult struct {
	Diagnostics []Diagnostic
	// Gap is set when the data needs a feature this implementation lacks.
	Gap string
}

// Error returns the first FAIL, or nil.
func (r *OpenResult) Error() *Diagnostic {
	for i := range r.Diagnostics {
		if r.Diagnostics[i].Severity == Fail {
			return &r.Diagnostics[i]
		}
	}
	return nil
}

func fileLines(text string) []string {
	parts := strings.Split(text, "\n")
	if len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	for i, p := range parts {
		parts[i] = strings.TrimSuffix(p, "\r")
	}
	return parts
}

func decodeExternal(t *Table, data []byte, out *[]Diagnostic, what string) (string, bool) {
	enc := t.Encoding
	if bom, n := SniffBOM(data); bom != "" {
		if v, ok := t.Keys["encoding"]; ok && !sameEncoding(v, bom) {
			*out = append(*out, newDiag("encoding-mismatch", fmt.Sprintf("%s: encoding=%s but the BOM says %s", what, v, bom), t.Line, t.Index))
		}
		enc = bom
		data = data[n:]
	}
	dec := DecoderFor(enc)
	if dec == nil {
		dec = DecoderFor("UTF-8")
	}
	s, err := dec(data)
	if err != nil {
		*out = append(*out, newDiag("encoding-invalid-bytes", fmt.Sprintf("%s does not decode as %s", what, enc), t.Line, t.Index))
		return "", false
	}
	return s, true
}

// OpenTable opens a table's external data into t.Data; inline tables are already open.
func OpenTable(t *Table, src Source) (res *OpenResult) {
	res = &OpenResult{}
	if t.Reference == "" {
		return res
	}
	defer func() {
		if r := recover(); r != nil {
			if g, ok := r.(*GapError); ok {
				res.Gap = g.What
				return
			}
			panic(r)
		}
	}()
	if t.Storage == "columns" {
		openColumns(t, src, &res.Diagnostics)
	} else {
		openRows(t, src, &res.Diagnostics)
	}
	return res
}

func mustRead(src Source, path string) ([]byte, bool) {
	b, ok, err := src.Read(path)
	if err != nil {
		if g, isGap := err.(*GapError); isGap {
			panic(g)
		}
		return nil, false
	}
	return b, ok
}

func openRows(t *Table, src Source, out *[]Diagnostic) {
	data, ok := mustRead(src, t.Reference)
	if !ok {
		*out = append(*out, newDiag("reference-not-found", t.Reference+" is missing; the table is metadata-only", t.Line, t.Index))
		return
	}
	text, ok := decodeExternal(t, data, out, t.Reference)
	if !ok {
		return
	}
	delim, quote := DelimChars(t.Delim), QuoteChar(t.Quote)
	var rows [][]string
	for k, line := range fileLines(text) {
		r, err := SplitRecord(line, delim, quote)
		if err != nil {
			*out = append(*out, newDiag("quoted-value-raw-newline", fmt.Sprintf("%s line %d: a quoted value runs past the end of the line", t.Reference, k+1), t.Line, t.Index))
			return
		}
		rows = append(rows, r)
	}
	var header []string
	if t.Header == 1 {
		header = []string{}
		if len(rows) > 0 {
			header, rows = rows[0], rows[1:]
		}
	}
	t.Data = &TableData{Header: header, Records: rows}
	t.RowsRead = len(rows)
	t.Width = -1
	if header != nil {
		t.Width = len(header)
	} else if len(rows) > 0 {
		t.Width = len(rows[0])
	}
	for k, r := range rows {
		if t.Width >= 0 && len(r) != t.Width {
			*out = append(*out, newDiag("record-arity-mismatch", fmt.Sprintf("%s record %d: %d fields, expected %d", t.Reference, k, len(r), t.Width), t.Line, t.Index))
			return
		}
	}
	if len(rows) != t.Rows {
		*out = append(*out, newDiag("rows-mismatch", fmt.Sprintf("rows=%d but %s has %d records", t.Rows, t.Reference, len(rows)), t.Line, t.Index))
	}
	if t.ColumnsDeclared >= 0 && t.Width >= 0 && t.Width != t.ColumnsDeclared {
		*out = append(*out, newDiag("columns-mismatch", fmt.Sprintf("columns=%d but %s has %d", t.ColumnsDeclared, t.Reference, t.Width), t.Line, t.Index))
	}
	for _, c := range t.Columns {
		if !c.Physical() {
			continue
		}
		if t.Header == 1 && header != nil {
			want := c.Name
			if c.HasTitle {
				want = c.Title
			}
			c.Cell = indexOf(header, want)
		} else {
			c.Cell = c.Index
		}
	}
	*out = append(*out, ResolveAddresses(t)...)
	*out = append(*out, DataChecks(t)...)
}

var reColFile = regexp.MustCompile(`^(\d+)(?:-([A-Za-z_][A-Za-z0-9_]*))?(\.col|/)$`)
var reSection = regexp.MustCompile(`^\d+\.col$`)

type colFile struct {
	entry string
	index int
	name  string
}

func openColumns(t *Table, src Source, out *[]Diagnostic) {
	dir := t.Reference
	listing, ok, _ := src.List(dir)
	if !ok {
		*out = append(*out, newDiag("reference-not-found", dir+" is missing; the table is metadata-only", t.Line, t.Index))
		return
	}
	sectioned := t.SectionSize > 0
	var files []colFile
	for _, n := range listing {
		m := reColFile.FindStringSubmatch(n)
		if m == nil || (m[3] == "/") != sectioned {
			continue
		}
		idx, _ := strconv.Atoi(m[1])
		files = append(files, colFile{n, idx, m[2]})
	}
	k := 0
	for _, c := range t.Columns {
		if !c.Physical() {
			continue
		}
		want := c.Index
		if want < 0 {
			want = k
		}
		k++
		found := -1
		for i, f := range files {
			if (c.Name != "" && f.name == c.Name) || (c.Name == "" && f.index == want) {
				found = i
				break
			}
		}
		if found < 0 {
			label := c.Name
			if label == "" {
				label = itoa(want)
			}
			*out = append(*out, newDiag("col-file-missing", fmt.Sprintf("%s: no .col file for column %s", dir, label), c.Line, t.Index))
			return
		}
		c.Cell = files[found].index
	}
	if t.ColumnsDeclared >= 0 && len(files) != t.ColumnsDeclared {
		*out = append(*out, newDiag("col-count-mismatch", fmt.Sprintf("%s has %d columns, columns=%d", dir, len(files), t.ColumnsDeclared), t.Line, t.Index))
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].index < files[j].index })
	decodeCol := func(path string) ([]string, bool) {
		b, ok := mustRead(src, path)
		if !ok {
			return nil, false
		}
		text, ok := decodeExternal(t, b, out, path)
		if !ok {
			return nil, false
		}
		return fileLines(text), true
	}
	var columns [][]string
	if !sectioned {
		for _, f := range files {
			vals, ok := decodeCol(dir + f.entry)
			if !ok {
				return
			}
			if len(vals) != t.Rows {
				*out = append(*out, newDiag("col-line-count-mismatch", fmt.Sprintf("%s%s has %d lines, rows=%d", dir, f.entry, len(vals), t.Rows), t.Line, t.Index))
				return
			}
			columns = append(columns, vals)
		}
	} else {
		size := t.SectionSize
		starts := make([][]string, len(files))
		for i, f := range files {
			names, _, _ := src.List(dir + f.entry)
			for _, n := range names {
				if reSection.MatchString(n) {
					starts[i] = append(starts[i], n)
				}
			}
			sort.Strings(starts[i])
		}
		key := func(xs []string) string {
			var b strings.Builder
			for _, x := range xs {
				n, _ := strconv.Atoi(strings.TrimSuffix(x, ".col"))
				b.WriteString(itoa(n) + ",")
			}
			return b.String()
		}
		for _, s := range starts {
			if key(s) != key(starts[0]) {
				*out = append(*out, newDiag("section-boundary-mismatch", dir+": columns have different section boundaries", t.Line, t.Index))
				return
			}
		}
		for i, f := range files {
			var vals []string
			for j, sec := range starts[i] {
				part, ok := decodeCol(dir + f.entry + sec)
				if !ok {
					return
				}
				start, _ := strconv.Atoi(strings.TrimSuffix(sec, ".col"))
				want := size
				if t.Rows-j*size < want {
					want = t.Rows - j*size
				}
				if start != j*size || len(part) != want {
					*out = append(*out, newDiag("section-partition-error", fmt.Sprintf("%s%s%s: sections do not partition the rows by %d", dir, f.entry, sec, size), t.Line, t.Index))
					return
				}
				vals = append(vals, part...)
			}
			if len(vals) != t.Rows {
				*out = append(*out, newDiag("section-partition-error", fmt.Sprintf("%s%s: sections hold %d rows, rows=%d", dir, f.entry, len(vals), t.Rows), t.Line, t.Index))
				return
			}
			columns = append(columns, vals)
		}
	}
	records := make([][]string, t.Rows)
	for r := range records {
		rec := make([]string, len(columns))
		for c := range columns {
			rec[c] = columns[c][r]
		}
		records[r] = rec
	}
	t.Data = &TableData{Records: records}
	t.RowsRead = t.Rows
	t.Width = len(columns)
	*out = append(*out, ResolveAddresses(t)...)
	*out = append(*out, DataChecks(t)...)
}
