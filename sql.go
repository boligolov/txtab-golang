package txtab

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

// ApplySQL returns the statements for a target dialect in execution order (pre-data in file order, then
// post-data) and the warnings. section is "", "pre-data" or "post-data".
func ApplySQL(doc *Doc, target string, strict bool, section string) ([]*SQLLine, []Diagnostic) {
	var out []*SQLLine
	var warnings []Diagnostic
	for _, sec := range []string{"pre-data", "post-data"} {
		if section != "" && section != sec {
			continue
		}
		for _, l := range doc.SQL {
			if l.Section != sec {
				continue
			}
			switch MatchKind(l, target) {
			case "none":
				continue
			case "family":
				warnings = append(warnings, newDiag("sql-dialect-family", l.Token+" for target "+target, l.Line, l.Table))
			case "version-mismatch":
				warnings = append(warnings, newDiag("sql-version-mismatch", l.Token+" for target "+target, l.Line, l.Table))
				if strict {
					continue
				}
			}
			out = append(out, l)
		}
	}
	if len(out) == 0 {
		warnings = append(warnings, newDiag("sql-no-match", "no SQL line matches "+target, 0, -1))
	}
	return out, warnings
}
