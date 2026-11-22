// Package txtab reads and checks Text Tables (.txtab, .txtab.zip): plain text tables that describe themselves.
//
//	f, err := txtab.Load("shop.txtab")   // opens reference= files and column directories too
//	if !f.OK() { fmt.Println(f.Doc.Error) }
//	orders := f.Doc.Table("orders")
//	fmt.Println(orders.Rows, orders.Meta["grain"], orders.Aggregates["sum"])
//
// Values stay strings as written (after CSV unquoting); nulls are empty fields or the table's null= marker.
// The specification is at https://texttables.dev.
package txtab

import (
	"os"
	"path/filepath"
	"strings"
)

// Version of this library; SpecVersion is the Text Tables version it implements.
const (
	Version     = "0.1.0"
	SpecVersion = "0.1"
)

// Loaded is a file read from disk with its external data opened: what `txtab verify` checks.
type Loaded struct {
	Path    string
	Doc     *Doc
	Archive *ArchiveResult
	Opened  map[int]*OpenResult
}

// Diagnostics returns the parse diagnostics followed by those found opening external data.
func (l *Loaded) Diagnostics() []Diagnostic {
	out := append([]Diagnostic{}, l.Doc.Diagnostics...)
	for i := range l.Doc.Tables {
		if r := l.Opened[i]; r != nil {
			for _, d := range r.Diagnostics {
				dup := false
				for _, x := range out {
					dup = dup || x == d
				}
				if !dup {
					out = append(out, d)
				}
			}
		}
	}
	return out
}

// Gap names a spec-allowed feature this implementation lacks, needed by the file ("" if none).
func (l *Loaded) Gap() string {
	if l.Archive != nil && l.Archive.Gap != "" {
		return l.Archive.Gap
	}
	for _, r := range l.Opened {
		if r.Gap != "" {
			return r.Gap
		}
	}
	return ""
}

// OK: parsed, external data opened, no FAIL anywhere.
func (l *Loaded) OK() bool {
	if !l.Doc.OK || l.Gap() != "" {
		return false
	}
	for _, d := range l.Diagnostics() {
		if d.Severity == Fail {
			return false
		}
	}
	return true
}

// Verified: OK, and no warning that `txtab verify` escalates.
func (l *Loaded) Verified() bool { return l.OK() && VerifyOK(l.Doc, l.Diagnostics()...) }

// Load reads a .txtab or .txtab.zip; with openData it also opens every reference= and column directory.
func Load(path string, openData bool) (*Loaded, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	abs, _ := filepath.Abs(path)
	outside := DirSource{Base: filepath.Dir(abs)}
	l := &Loaded{Path: path, Opened: map[int]*OpenResult{}}
	var src Source = outside
	if strings.HasSuffix(strings.ToLower(path), ".zip") {
		l.Archive = ParseArchive(data, filepath.Base(path), outside)
		l.Doc = l.Archive.Doc
		src = l.Archive.Source
		for k, v := range l.Archive.Opened {
			l.Opened[k] = v
		}
	} else {
		l.Doc = Parse(data)
	}
	if openData && l.Doc.OK && src != nil {
		for _, t := range l.Doc.Tables {
			if t.Reference != "" && l.Opened[t.Index] == nil {
				l.Opened[t.Index] = OpenTable(t, src)
			}
		}
	}
	return l, nil
}
