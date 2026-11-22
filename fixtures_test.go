package txtab

// Runs the shared corpus (texttables/fixtures, plan/02-fixtures.md). The manifest is read from fixtures.json, the
// JSON mirror of fixtures.yaml, so the tests need no YAML library. The corpus is found at $TXTAB_FIXTURES, else
// ../texttables/fixtures next to this repository.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func fixturesRoot() string {
	if r := os.Getenv("TXTAB_FIXTURES"); r != "" {
		return r
	}
	return filepath.Join("..", "texttables", "fixtures")
}

type manifest struct {
	ErrorKinds []string         `json:"error_kinds"`
	Entries    []map[string]any `json:"entries"`
}

func loadManifest(t *testing.T) manifest {
	b, err := os.ReadFile(filepath.Join(fixturesRoot(), "fixtures.json"))
	if err != nil {
		t.Skipf("fixtures not found (%v); set TXTAB_FIXTURES", err)
	}
	var m manifest
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func js(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func sortedCodes(ds []Diagnostic, sev Severity) []string {
	out := []string{}
	for _, d := range ds {
		if d.Severity == sev {
			out = append(out, d.Code)
		}
	}
	sort.Strings(out)
	return out
}

func sortedAny(v any) []string {
	out := []string{}
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	sort.Strings(out)
	return out
}

func optInt(n int) any {
	if n < 0 {
		return nil
	}
	return n
}

func optStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func TestErrorKindsMatchRegistry(t *testing.T) {
	m := loadManifest(t)
	var reg []string
	for c := range Codes {
		reg = append(reg, c)
	}
	sort.Strings(reg)
	kinds := append([]string{}, m.ErrorKinds...)
	sort.Strings(kinds)
	if js(reg) != js(kinds) {
		t.Errorf("registry %v\nmanifest %v", reg, kinds)
	}
}

func TestFixtures(t *testing.T) {
	m := loadManifest(t)
	root := fixturesRoot()
	for _, e := range m.Entries {
		if e["superseded_by"] != nil {
			continue
		}
		id := e["id"].(string)
		t.Run(id, func(t *testing.T) {
			if gap := checkFixture(t, root, id, e); gap != "" {
				t.Skip(gap)
			}
		})
	}
}

// checkFixture reports mismatches with t.Errorf and returns a non-empty gap when the fixture needs a missing feature.
func checkFixture(t *testing.T, root, id string, e map[string]any) string {
	x, _ := e["expect"].(map[string]any)
	if x == nil {
		x = map[string]any{}
	}
	eq := func(what string, got, want any) {
		t.Helper()
		if js(got) != js(want) {
			t.Errorf("%s: got %s, want %s", what, js(got), js(want))
		}
	}
	path := filepath.Join(root, filepath.FromSlash(id))
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	dir := DirSource{Base: filepath.Dir(path)}
	var doc *Doc
	var src Source = dir
	var comment *CommentInfo
	opened := map[int]*OpenResult{}
	if strings.HasPrefix(id, "archive/") {
		r := ParseArchive(data, filepath.Base(path), dir)
		if r.Gap != "" {
			return r.Gap
		}
		doc, src, comment = r.Doc, r.Source, &r.Comment
		for k, v := range r.Opened {
			opened[k] = v
		}
	} else {
		doc = Parse(data)
	}
	defer func() {
		if t.Failed() && doc.Error != nil {
			t.Logf("parser: %s", doc.Error)
		}
	}()
	if x["recognized"] == false {
		eq("recognized", doc.Recognized, false)
		return ""
	}
	eq("recognized", doc.Recognized, true)
	if v, ok := x["parse"]; ok {
		got := "fail"
		if doc.OK {
			got = "ok"
		}
		eq("parse", got, v)
	}
	if v, ok := x["error_kind"]; ok {
		var got any
		if doc.Error != nil {
			got = doc.Error.Code
		}
		eq("error_kind", got, v)
	}
	if v, ok := x["warnings"]; ok {
		eq("warnings", sortedCodes(doc.Diagnostics, Warn), sortedAny(v))
	}
	if v, ok := x["verify"]; ok {
		got := "fail"
		if VerifyOK(doc) {
			got = "ok"
		}
		eq("verify", got, v)
	}
	if v, ok := x["tables_read"]; ok {
		eq("tables_read", len(doc.Tables), v)
	}
	for _, k := range []struct {
		key string
		got string
	}{{"prefix", doc.Prefix}, {"version", doc.Version}, {"encoding", doc.Encoding}} {
		if v, ok := x[k.key]; ok {
			eq(k.key, k.got, v)
		}
	}
	if fl, ok := x["first_line"].(map[string]any); ok {
		for k, v := range fl {
			eq("first_line."+k, doc.FirstLine[k], v)
		}
	}
	if v, ok := x["meta"]; ok {
		eq("meta", doc.Meta, v)
	}
	if v, ok := x["notes"]; ok {
		var got []map[string]string
		for _, n := range doc.Notes {
			got = append(got, map[string]string{"target": n.Target})
		}
		eq("notes", got, v)
	}

	if doc.OK && src != nil {
		for _, tb := range doc.Tables {
			if tb.Reference != "" && opened[tb.Index] == nil {
				opened[tb.Index] = OpenTable(tb, src)
			}
		}
		for _, r := range opened {
			if r.Gap != "" {
				return r.Gap
			}
		}
	}

	tables, _ := x["tables"].([]any)
	for k, w := range tables {
		want := w.(map[string]any)
		p := fmt.Sprintf("tables[%d]", k)
		if k >= len(doc.Tables) {
			t.Errorf("%s missing", p)
			continue
		}
		tb := doc.Tables[k]
		physical := 0
		for _, c := range tb.Columns {
			if c.Physical() {
				physical++
			}
		}
		simple := map[string]any{
			"name": optStr(tb.Name), "rows": tb.Rows, "rows_read": optInt(tb.RowsRead), "storage": tb.Storage,
			"reference": optStr(tb.Reference), "columns": physical, "delim": tb.Delim, "encoding": tb.Encoding,
			"header": strconv.Itoa(tb.Header), "data_size": optInt(tb.DataSize),
		}
		for key, v := range want {
			if s, ok := simple[key]; ok {
				eq(p+"."+key, s, v)
				continue
			}
			sub, _ := v.(map[string]any)
			switch key {
			case "meta":
				if len(sub) == 0 {
					eq(p+".meta", tb.Meta, map[string]string{})
				}
				for mk, mv := range sub {
					eq(p+".meta."+mk, tb.Meta[mk], mv)
				}
			case "aggregates":
				for a, vals := range sub {
					eq(p+".aggregates."+a, tb.Aggregates[a], vals)
				}
			case "computed":
				var virt, mat []string
				for _, c := range tb.Columns {
					if c.Computed && c.Materialized {
						mat = append(mat, c.Name)
					} else if c.Computed {
						virt = append(virt, c.Name)
					}
				}
				if w, ok := sub["virtual"]; ok {
					eq(p+".computed.virtual", virt, w)
				}
				if w, ok := sub["materialized"]; ok {
					eq(p+".computed.materialized", mat, w)
				}
			case "charts":
				var types, engines []string
				for _, c := range tb.Charts {
					if c.Engine == "" {
						types = append(types, c.Type)
					} else {
						engines = append(engines, c.Engine)
					}
				}
				if w, ok := sub["count"]; ok {
					eq(p+".charts.count", len(tb.Charts), w)
				}
				if w, ok := sub["types"]; ok {
					eq(p+".charts.types", types, w)
				}
				if w, ok := sub["engines"]; ok {
					eq(p+".charts.engines", engines, w)
				}
			case "notes":
				var targets, texts []string
				var rows []any
				for _, n := range tb.Notes {
					targets = append(targets, n.Target)
					texts = append(texts, n.Text)
					rows = append(rows, optInt(n.ResolvedRow))
				}
				if w, ok := sub["count"]; ok {
					eq(p+".notes.count", len(tb.Notes), w)
				}
				if w, ok := sub["targets"]; ok {
					eq(p+".notes.targets", targets, w)
				}
				if w, ok := sub["resolved_rows"]; ok {
					eq(p+".notes.resolved_rows", rows, w)
				}
				if w, ok := sub["texts"]; ok {
					eq(p+".notes.texts", texts, w)
				}
			case "links":
				if w, ok := sub["count"]; ok {
					eq(p+".links.count", len(tb.Links), w)
				}
			default:
				t.Errorf("%s.%s: the runner does not check this field", p, key)
			}
		}
	}

	if sq, ok := x["sql"].(map[string]any); ok {
		for sec, n := range sq {
			count := 0
			for _, l := range doc.SQL {
				if l.Section == strings.ReplaceAll(sec, "_", "-") {
					count++
				}
			}
			eq("sql."+sec, count, n)
		}
	}
	if ap, ok := x["apply"].(map[string]any); ok {
		lines, warnings := ApplySQL(doc, ap["target"].(string), false, "")
		if w, ok := ap["order"]; ok {
			var got []int
			for _, l := range lines {
				got = append(got, l.Line)
			}
			eq("apply.order", got, w)
		}
		if w, ok := ap["warnings"]; ok {
			eq("apply.warnings", sortedCodes(warnings, Warn), sortedAny(w))
		}
	}
	if op, ok := x["open"].(map[string]any); ok {
		for key, w := range op {
			want := w.(map[string]any)
			tb := doc.Table(key)
			if tb == nil {
				if n, err := strconv.Atoi(key); err == nil && n < len(doc.Tables) {
					tb = doc.Tables[n]
				}
			}
			if tb == nil {
				t.Errorf("open.%s: no such table", key)
				continue
			}
			r := opened[tb.Index]
			if r == nil {
				t.Errorf("open.%s: not opened", key)
				continue
			}
			if v, ok := want["rows_read"]; ok {
				eq("open."+key+".rows_read", tb.RowsRead, v)
			}
			if v, ok := want["warnings"]; ok {
				eq("open."+key+".warnings", sortedCodes(r.Diagnostics, Warn), sortedAny(v))
			}
			if e := r.Error(); e != nil {
				t.Errorf("open.%s: %s", key, e)
			}
		}
	}
	if c, ok := x["comment"].(map[string]any); ok {
		if comment == nil {
			t.Errorf("comment: not an archive")
			return ""
		}
		if v, ok := c["truncated"]; ok {
			eq("comment.truncated", comment.Truncated, v)
		}
		if v, ok := c["flag_line"]; ok {
			eq("comment.flag_line", optInt(comment.FlagLine), v)
		}
		if v, ok := c["kept_tables"]; ok {
			var kept []any
			for _, n := range comment.KeptTables {
				kept = append(kept, optStr(n))
			}
			eq("comment.kept_tables", kept, v)
		}
		if v, ok := c["contains_comments"]; ok {
			eq("comment.contains_comments", comment.ContainsComments, v)
		}
		if v, ok := c["equals_main_without_data"]; ok {
			eq("comment.equals_main_without_data", comment.EqualsMainWithoutData, v)
		}
		if v, ok := c["max_bytes"]; ok && float64(comment.Bytes) > v.(float64) {
			t.Errorf("comment: %d bytes > %v", comment.Bytes, v)
		}
		has := map[string]bool{}
		for _, k := range comment.Keywords {
			has[k] = true
		}
		if v, ok := c["includes"].([]any); ok {
			for _, k := range v {
				if !has[k.(string)] {
					t.Errorf("comment: missing %s", k)
				}
			}
		}
		if v, ok := c["excludes"].([]any); ok {
			for _, k := range v {
				if has[k.(string)] {
					t.Errorf("comment: has %s", k)
				}
			}
		}
	}
	return ""
}
