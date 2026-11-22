package txtab

import "strings"

// MatchKind classifies a SQL line for a target dialect: exact, family, version-mismatch or none.
func MatchKind(line *SQLLine, target string) string {
	dialect, version, _ := SplitDialect(target)
	switch {
	case line.Dialect != dialect:
		return "none"
	case line.Version == version:
		return "exact"
	case line.Version == "" || version == "":
		return "family"
	}
	return "version-mismatch"
}

// Statement is one inline statement, or the whole script read from a line's reference= file.
type Statement struct {
	Line *SQLLine
	Text string
}

// Applied is the SQL for a target dialect.
type Applied struct {
	// Statements in execution order: pre-data in file order, then post-data.
	Statements []Statement
	// References are the reference= files read, in order.
	References []string
	Warnings   []Diagnostic
}

// Lines returns the lines that produced the statements.
func (a *Applied) Lines() []*SQLLine {
	out := make([]*SQLLine, len(a.Statements))
	for i, s := range a.Statements {
		out[i] = s.Line
	}
	return out
}

// ReadScript reads a line's reference= script (UTF-8, BOM stripped); ok is false when it is missing.
func ReadScript(line *SQLLine, src Source) (string, bool) {
	if line.Reference == "" || src == nil {
		return "", false
	}
	b, ok, err := src.Read(line.Reference)
	if err != nil || !ok {
		return "", false
	}
	return strings.TrimPrefix(string(b), "\ufeff"), true
}

// CheckSQLReferences reports sql-reference-not-found for every reference= script that cannot be read.
func CheckSQLReferences(doc *Doc, src Source) []Diagnostic {
	var out []Diagnostic
	for _, l := range doc.SQL {
		if l.Reference == "" {
			continue
		}
		if _, ok := ReadScript(l, src); !ok {
			out = append(out, newDiag("sql-reference-not-found", l.Reference+" is missing; the line is skipped", l.Line, l.Table))
		}
	}
	return out
}

// ApplySQL returns the statements for a target dialect, a referenced script in place of its line. section is "",
// "pre-data" or "post-data"; src reads reference= scripts (nil: none can be read).
func ApplySQL(doc *Doc, target string, strict bool, section string, src Source) *Applied {
	out := &Applied{}
	matched := false
	for _, sec := range []string{"pre-data", "post-data"} {
		if section != "" && section != sec {
			continue
		}
		for _, l := range doc.SQL {
			if l.Section != sec {
				continue
			}
			kind := MatchKind(l, target)
			if kind == "none" {
				continue
			}
			matched = true
			switch kind {
			case "family":
				out.Warnings = append(out.Warnings, newDiag("sql-dialect-family", l.Token+" for target "+target, l.Line, l.Table))
			case "version-mismatch":
				out.Warnings = append(out.Warnings, newDiag("sql-version-mismatch", l.Token+" for target "+target, l.Line, l.Table))
				if strict {
					continue
				}
			}
			if l.Reference != "" {
				text, ok := ReadScript(l, src)
				if !ok {
					out.Warnings = append(out.Warnings, newDiag("sql-reference-not-found", l.Reference+" is missing; the line is skipped", l.Line, l.Table))
					continue
				}
				out.References = append(out.References, l.Reference)
				out.Statements = append(out.Statements, Statement{l, text})
				continue
			}
			out.Statements = append(out.Statements, Statement{l, l.Statement})
		}
	}
	if !matched {
		out.Warnings = append(out.Warnings, newDiag("sql-no-match", "no SQL line matches "+target, 0, -1))
	}
	return out
}
