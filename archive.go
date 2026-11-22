package txtab

import (
	"archive/zip"
	"bytes"
	"compress/bzip2"
	"fmt"
	"io"
	"path"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// MethodNames are the ZIP compression methods the spec allows (archive.md#compression-and-encryption).
var MethodNames = map[uint16]string{0: "Store", 8: "Deflate", 9: "Deflate64", 12: "BZIP2", 14: "LZMA", 93: "Zstandard"}

// decodedMethods are the ones this implementation reads.
var decodedMethods = map[uint16]bool{0: true, 8: true, 12: true, 14: true}

// CommentInfo describes an archive's ZIP comment.
type CommentInfo struct {
	Present               bool
	Bytes                 int
	Valid                 bool
	Text                  string
	Truncated             bool
	FlagLine              int // 0 when absent
	KeptTables            []string
	ContainsComments      bool
	EqualsMainWithoutData bool
	Keywords              []string
}

// ArchiveResult is a parsed .txtab.zip.
type ArchiveResult struct {
	Doc      *Doc
	Entries  []string
	MainName string
	Comment  CommentInfo
	// Source reads archive entries first, then next to the archive.
	Source Source
	Gap    string
	// Opened holds the storage=columns tables opened during verification, by table index.
	Opened map[int]*OpenResult
}

func failDoc(code, msg string) *Doc {
	d := newDiag(code, msg, 0, -1)
	return &Doc{Recognized: true, Error: &d, Diagnostics: []Diagnostic{d}, FirstLine: map[string]string{}, Meta: map[string]string{}}
}

type archiveSource struct {
	byName  map[string]*zip.File
	names   []string
	base    string
	outside Source
}

func (s *archiveSource) Read(p string) ([]byte, bool, error) {
	f := s.byName[path.Clean(s.base+p)]
	if f != nil && !strings.HasSuffix(f.Name, "/") {
		if f.Flags&1 != 0 {
			return nil, false, &GapError{f.Name + " is encrypted"}
		}
		if !decodedMethods[f.Method] {
			return nil, false, &GapError{MethodNames[f.Method] + " is not implemented here"}
		}
		b, err := readEntry(f)
		return b, err == nil, err
	}
	if s.outside != nil {
		return s.outside.Read(p)
	}
	return nil, false, nil
}

func (s *archiveSource) List(dir string) ([]string, bool, error) {
	prefix := path.Clean(s.base+dir) + "/"
	seen := map[string]bool{}
	var out []string
	for _, n := range s.names {
		if !strings.HasPrefix(n, prefix) || n == prefix {
			continue
		}
		rest := n[len(prefix):]
		if k := strings.IndexByte(rest, '/'); k >= 0 {
			rest = rest[:k+1]
		}
		if !seen[rest] {
			seen[rest] = true
			out = append(out, rest)
		}
	}
	if len(out) > 0 {
		sort.Strings(out)
		return out, true, nil
	}
	if s.outside != nil {
		return s.outside.List(dir)
	}
	return nil, false, nil
}

// ParseArchive parses a .txtab.zip. archiveName is its file name (for the main-file rule); outside reads next to it.
func ParseArchive(data []byte, archiveName string, outside Source) *ArchiveResult {
	res := &ArchiveResult{Opened: map[int]*OpenResult{}}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		res.Doc = failDoc("zip-main-not-first", err.Error())
		res.Doc.Recognized = false
		return res
	}
	zr.RegisterDecompressor(12, func(r io.Reader) io.ReadCloser { return io.NopCloser(bzip2.NewReader(r)) })
	for _, f := range zr.File {
		res.Entries = append(res.Entries, f.Name)
	}
	if len(zr.File) == 0 || strings.HasSuffix(zr.File[0].Name, "/") || !strings.HasSuffix(zr.File[0].Name, ".txtab") {
		first := "missing"
		if len(zr.File) > 0 {
			first = zr.File[0].Name
		}
		res.Doc = failDoc("zip-main-not-first", "the first entry is "+first+", not a .txtab main file")
		return res
	}
	main := zr.File[0]
	base := regexp.MustCompile(`(?i)\.zip$`).ReplaceAllString(path.Base(strings.ReplaceAll(archiveName, `\`, "/")), "")
	if b := path.Base(main.Name); b != base && b != "data.txtab" {
		res.Doc = failDoc("zip-main-bad-name", fmt.Sprintf("main file %s; expected %s or data.txtab", main.Name, base))
		return res
	}
	if main.Flags&1 != 0 {
		res.Doc = failDoc("zip-encrypted", main.Name+" is encrypted and no password was given")
		return res
	}
	if _, allowed := MethodNames[main.Method]; !allowed {
		res.Doc = failDoc("zip-unsupported-compression", fmt.Sprintf("%s uses compression method %d", main.Name, main.Method))
		return res
	}
	if !decodedMethods[main.Method] {
		res.Doc = &Doc{Recognized: true, FirstLine: map[string]string{}, Meta: map[string]string{}}
		res.Gap = MethodNames[main.Method] + " decompression is not implemented here"
		return res
	}
	body, err := readEntry(main)
	if err != nil {
		res.Doc = failDoc("zip-main-not-first", err.Error())
		return res
	}
	doc := Parse(body)
	res.Doc = doc
	res.MainName = main.Name
	src := &archiveSource{byName: map[string]*zip.File{}, base: main.Name[:strings.LastIndex(main.Name, "/")+1], outside: outside}
	for _, f := range zr.File {
		src.byName[f.Name] = f
		src.names = append(src.names, f.Name)
	}
	res.Source = src
	if !doc.OK {
		return res
	}
	for _, t := range doc.Tables {
		if t.Storage != "columns" {
			continue
		}
		r := OpenTable(t, src)
		res.Opened[t.Index] = r
		if r.Gap != "" {
			res.Gap = r.Gap
			continue
		}
		if e := r.Error(); e != nil {
			doc.OK = false
			doc.Error = e
			doc.Diagnostics = append(doc.Diagnostics, *e)
			return res
		}
	}
	res.Comment = readComment([]byte(zr.Comment), doc)
	return res
}

// readEntry returns an entry's bytes; LZMA (method 14) is decoded here, the rest by archive/zip.
func readEntry(f *zip.File) ([]byte, error) {
	if f.Method == 14 {
		raw, err := f.OpenRaw()
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(raw)
		if err != nil {
			return nil, err
		}
		return decodeZipLZMA(data, f.UncompressedSize64)
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

var reTableName = regexp.MustCompile(`(?:^| )name=(\S+)`)

func readComment(raw []byte, doc *Doc) CommentInfo {
	info := CommentInfo{}
	if len(raw) == 0 {
		return info
	}
	info.Present = true
	info.Bytes = len(raw)
	add := func(code, msg string) { doc.Diagnostics = append(doc.Diagnostics, newDiag(code, msg, 0, -1)) }
	if !utf8.Valid(raw) {
		add("zip-comment-not-utf8", "the archive comment is not UTF-8")
		return info
	}
	info.Text = string(raw)
	lines := fileLines(info.Text)
	P := doc.Prefix
	isSys := func(x string) bool { return x == P || strings.HasPrefix(x, P+" ") }
	kw := func(x string) string {
		if x == P {
			return "--"
		}
		rest := x[len(P)+1:]
		if k := strings.IndexByte(rest, ' '); k >= 0 {
			return rest[:k]
		}
		return rest
	}
	allSys := true
	for _, x := range lines {
		allSys = allSys && isSys(x)
	}
	if len(lines) == 0 || !strings.HasPrefix(lines[0], P+" texttables=") || !allSys {
		add("zip-comment-not-prefix", "the archive comment is not a Text Tables prefix of the main file")
		return info
	}
	kws := map[string]bool{}
	for _, x := range lines[1:] {
		kws[kw(x)] = true
		info.ContainsComments = info.ContainsComments || kw(x) == "--"
	}
	for k := range kws {
		info.Keywords = append(info.Keywords, k)
	}
	sort.Strings(info.Keywords)
	flag := -1
	for k, x := range lines {
		if x == P+" meta-comment-truncated" {
			flag = k
			break
		}
	}
	info.Truncated = flag >= 0
	if flag >= 0 {
		info.FlagLine = flag + 1
	}
	for _, x := range lines {
		if kw(x) == "table" {
			name := ""
			if m := reTableName.FindStringSubmatch(x[len(P)+7:]); m != nil {
				name = m[1]
			}
			info.KeptTables = append(info.KeptTables, name)
		}
	}
	var mainAll, main []string
	for _, s := range doc.SystemLines {
		mainAll = append(mainAll, s.Text)
		if s.Keyword != "--" {
			main = append(main, s.Text)
		}
	}
	info.EqualsMainWithoutData = strings.Join(lines, "\n") == strings.Join(mainAll, "\n")
	var body []string
	for k, x := range lines {
		if !(k == flag && flag == 1) {
			body = append(body, x)
		}
	}
	j, sub := 0, true
	for _, x := range body {
		for j < len(main) && main[j] != x {
			j++
		}
		if j == len(main) {
			sub = false
			break
		}
		j++
	}
	tablesKept := true
	for _, s := range doc.SystemLines {
		if s.Keyword == "table" && indexOf(body, s.Text) < 0 {
			tablesKept = false
		}
	}
	complete := len(body) == len(main)
	if !sub || len(body) == 0 || body[0] != main[0] || !tablesKept || (!info.Truncated && !complete) || (flag >= 0 && flag != 1) {
		add("zip-comment-disagree", "the archive comment disagrees with the main file")
		return info
	}
	info.Valid = true
	return info
}
