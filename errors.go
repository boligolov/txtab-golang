package txtab

import "fmt"

// Severity of a diagnostic (spec/errors.md).
type Severity string

const (
	// Fail: a conforming parser must reject the file.
	Fail Severity = "FAIL"
	// Warn: a conforming parser must not reject the file.
	Warn Severity = "WARN"
)

// VerifyRule says what `txtab verify` does with a code: "-" unchanged, "fail" escalated, "never" stays a warning.
type VerifyRule string

type codeInfo struct {
	Severity Severity
	Verify   VerifyRule
}

// Codes is the error-code registry of spec/errors.md.
var Codes = map[string]codeInfo{}

func reg(s Severity, v VerifyRule, codes ...string) {
	for _, c := range codes {
		Codes[c] = codeInfo{s, v}
	}
}

func init() {
	// file and first line
	reg(Fail, "-", "prefix-invalid", "kv-malformed", "value-invalid", "first-line-not-ascii", "encoding-mixed-inline", "encoding-invalid-bytes")
	reg(Warn, "-", "key-unknown", "key-duplicate", "key-misplaced", "version-unknown", "encoding-unknown", "encoding-mismatch")
	// system lines
	reg(Fail, "-", "keyword-malformed", "column-in-file-block", "agg-in-file-block", "link-in-file-block", "chart-in-file-block", "note-address-in-file-block", "directive-after-data")
	reg(Warn, "-", "keyword-unknown", "meta-duplicate")
	// tables
	reg(Fail, "-", "tables-mismatch", "tables-missing", "table-missing-rows", "table-name-invalid", "table-name-duplicate", "data-in-reference-table", "reference-escapes-dir", "storage-columns-without-dir")
	reg(Warn, "fail", "rows-mismatch", "columns-mismatch")
	reg(Warn, "never", "reference-not-found")
	reg(Warn, "-", "section-size-without-columns")
	// data records
	reg(Fail, "-", "record-arity-mismatch", "quote-none-delimiter-in-value", "quoted-value-raw-newline")
	reg(Warn, "-", "record-starts-with-prefix")
	// columns
	reg(Fail, "-", "column-missing-name", "column-name-invalid", "column-missing-index", "column-malformed-attribute", "unit-unknown-column")
	reg(Warn, "-", "column-unknown-attribute", "column-name-header-mismatch", "column-title-header-mismatch", "column-duplicate", "references-unresolved")
	reg(Warn, "never", "column-count-mismatch", "default-with-nulls")
	// computed columns
	reg(Fail, "-", "formula-references-computed", "formula-unknown-reference", "formula-parse-error", "formula-index-forbidden", "formula-requires-header", "computed-materialized-mismatch")
	reg(Warn, "-", "computed-default-ignored")
	reg(Warn, "never", "computed-stale")
	// aggregates
	reg(Warn, "-", "agg-unknown", "agg-duplicate", "agg-type-mismatch")
	reg(Warn, "never", "agg-arity-mismatch", "agg-mismatch")
	// sql
	reg(Fail, "-", "sql-dialect-missing")
	reg(Warn, "-", "sql-unknown-dialect", "sql-dialect-family", "sql-version-mismatch", "sql-no-match")
	reg(Warn, "never", "ddl-column-mismatch")
	// notes and links
	reg(Fail, "-", "note-malformed", "note-missing-text", "note-row-and-key", "link-malformed", "link-missing-href", "link-missing-address", "link-row-and-key")
	reg(Warn, "never", "note-unresolved", "link-unresolved", "link-unsafe-scheme")
	reg(Warn, "-", "link-duplicate", "link-unknown-column", "link-template-malformed")
	// charts
	reg(Fail, "-", "chart-missing-type", "chart-missing-name", "chart-unknown-column", "chart-missing-required-channel", "chart-vega-invalid-json")
	reg(Warn, "-", "chart-duplicate-name", "chart-unknown-type", "chart-unknown-channel")
	// archive and column storage
	reg(Fail, "-", "zip-main-not-first", "zip-main-bad-name", "zip-unsupported-compression", "zip-encrypted", "col-file-missing", "col-count-mismatch", "col-line-count-mismatch", "section-partition-error", "section-boundary-mismatch")
	reg(Warn, "-", "zip-comment-not-utf8", "zip-comment-not-prefix", "zip-comment-disagree")
}

// Diagnostic is one error or warning.
type Diagnostic struct {
	Code     string
	Severity Severity
	Message  string
	// Line is 1-based (0: none).
	Line int
	// Table is the zero-based table index (-1: none).
	Table int
}

func (d Diagnostic) String() string {
	if d.Line > 0 {
		return fmt.Sprintf("line %d: %s %s: %s", d.Line, d.Severity, d.Code, d.Message)
	}
	return fmt.Sprintf("%s %s: %s", d.Severity, d.Code, d.Message)
}

func newDiag(code, msg string, line, table int) Diagnostic {
	info, ok := Codes[code]
	if !ok {
		panic("unregistered code " + code)
	}
	return Diagnostic{Code: code, Severity: info.Severity, Message: msg, Line: line, Table: table}
}

// parseFail stops the parser at the first FAIL.
type parseFail struct{ d Diagnostic }

func failf(code string, line, table int, format string, a ...any) {
	panic(parseFail{newDiag(code, fmt.Sprintf(format, a...), line, table)})
}
