package txtab

// Column is one `column` line.
type Column struct {
	Line         int
	Attrs        map[string]string
	Name         string // "" when absent
	Title        string // "" when absent
	HasTitle     bool
	Index        int // -1 when absent
	Type         string
	Formula      string
	Computed     bool
	Materialized bool
	// Cell is the bound physical position in the data (header cell or index), -1 when unknown.
	Cell int
}

// Physical: stored, or computed and materialized.
func (c *Column) Physical() bool { return !c.Computed || c.Materialized }

func (c *Column) label() string {
	if c.Name != "" {
		return c.Name
	}
	return itoa(c.Index)
}

// Note is a `note` line. Target is file, table, column, row or cell.
type Note struct {
	Line                     int
	Attrs                    map[string]string
	Text                     string
	Col, Row, Key            *string
	Target                   string
	ResolvedRow, ResolvedCol int // -1 when not resolved
	// Resolved: nil when not checked (data not opened).
	Resolved *bool
}

// Link is a `link` line.
type Link struct {
	Line                     int
	Attrs                    map[string]string
	Href, Col                string
	Row, Key                 *string
	ResolvedRow, ResolvedCol int
	Resolved                 *bool
}

// Chart is a `chart` or `chart-<engine>` line. Engine is "" for the compact form.
type Chart struct {
	Line    int
	Engine  string
	Attrs   map[string]string
	Type    string
	Name    string
	Payload string
	Known   bool
}

// SQLLine is a `sql-pre-data`, `sql-post-data` or `sql-dql` line.
type SQLLine struct {
	Line      int
	Section   string // pre-data, post-data, dql
	Token     string
	Dialect   string
	Version   string // "" when unversioned
	Known     bool
	Statement string // "" when the line names a script with reference=
	Reference string // the .sql file named by reference=, "" for an inline statement
	Table     int    // -1 for the file block
}

// TableData holds raw field values after CSV unquoting.
type TableData struct {
	Header  []string // nil without a header row
	Records [][]string
}

// Table is one `table` line and its block and data.
type Table struct {
	Index           int
	Line            int
	Keys            map[string]string
	Name            string // "" when unnamed
	Rows            int
	Header          int
	Delim           string
	Quote           string
	NullMarker      string // "" for none
	Encoding        string
	Reference       string // "" for inline data
	Storage         string
	SectionSize     int // 0 when absent
	DataSize        int // -1 when absent
	ColumnsDeclared int // -1 when absent
	Meta            map[string]string
	Columns         []*Column
	Aggregates      map[string][]string
	Notes           []*Note
	Links           []*Link
	Charts          []*Chart
	SQL             []*SQLLine
	// Data: inline data, or external data once opened.
	Data *TableData
	// RowsRead: records read, -1 when not opened.
	RowsRead int
	// Width: physical column count seen in the data, -1 when unknown.
	Width    int
	DataLine int
}

// Label is the table's name, or #index.
func (t *Table) Label() string {
	if t.Name != "" {
		return t.Name
	}
	return "#" + itoa(t.Index)
}

// IsNull: empty, or the table's null marker.
func (t *Table) IsNull(v string) bool { return v == "" || (t.NullMarker != "" && v == t.NullMarker) }

// SystemLine is every line except data records: what an archive comment is made of.
type SystemLine struct {
	Line    int
	Text    string
	Keyword string // texttables for line 1, -- for comments
	Table   int
}

// Doc is a parsed Text Tables file.
type Doc struct {
	Recognized bool
	OK         bool
	Error      *Diagnostic
	// Diagnostics: warnings (and the error, last) in the order found.
	Diagnostics []Diagnostic
	Prefix      string
	Version     string
	// Encoding: the BOM's, or encoding= (default UTF-8).
	Encoding    string
	BOM         string
	FirstLine   map[string]string
	Meta        map[string]string
	Notes       []*Note
	SQL         []*SQLLine
	Tables      []*Table
	SystemLines []SystemLine
	LineCount   int
}

// Warnings returns the WARN diagnostics.
func (d *Doc) Warnings() []Diagnostic {
	var out []Diagnostic
	for _, x := range d.Diagnostics {
		if x.Severity == Warn {
			out = append(out, x)
		}
	}
	return out
}

// Table finds a table by name.
func (d *Doc) Table(name string) *Table {
	for _, t := range d.Tables {
		if t.Name == name {
			return t
		}
	}
	return nil
}
