// Command txtab: verify, peek, sql and describe Text Tables files.
package main

import (
	"archive/zip"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/boligolov/txtab-golang"
)

const usage = `txtab — Text Tables (.txtab) tools

  txtab verify FILE...              errors and warnings; rows-mismatch and columns-mismatch fail
  txtab peek [--full] FILE          the system lines: the schema without the data (an archive: its ZIP comment)
  txtab sql [--section S] [--strict] DIALECT FILE
                                    SQL for a database, in execution order
  txtab describe [-o OUT] FILE.csv  describe a plain CSV/TSV as Text Tables
  txtab version
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var code int
	switch os.Args[1] {
	case "verify":
		code = verify(os.Args[2:])
	case "peek":
		code = peek(os.Args[2:])
	case "sql":
		code = sql(os.Args[2:])
	case "describe":
		code = describe(os.Args[2:])
	case "version", "--version":
		fmt.Printf("txtab %s (Text Tables %s)\n", txtab.Version, txtab.SpecVersion)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprint(os.Stderr, usage)
		code = 2
	}
	os.Exit(code)
}

func verify(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}
	worst := 0
	for _, path := range args {
		f, err := txtab.Load(path, true)
		if err != nil {
			fmt.Printf("%s: %v\n", path, err)
			worst = max(worst, 2)
			continue
		}
		if !f.Doc.Recognized {
			fmt.Printf("%s: not a Text Tables file\n", path)
			worst = max(worst, 2)
			continue
		}
		for _, d := range f.Diagnostics() {
			where := ""
			if d.Line > 0 {
				where = fmt.Sprintf(":%d", d.Line)
			}
			fmt.Printf("%s%s: %s %s: %s\n", path, where, d.Severity, d.Code, d.Message)
		}
		if g := f.Gap(); g != "" {
			fmt.Printf("%s: not checked: %s\n", path, g)
			worst = max(worst, 2)
			continue
		}
		if f.Verified() {
			var parts []string
			for _, t := range f.Doc.Tables {
				parts = append(parts, fmt.Sprintf("%s (%d rows)", t.Label(), t.Rows))
			}
			if len(parts) == 0 {
				parts = []string{"no tables"}
			}
			fmt.Printf("%s: ok — %s\n", path, strings.Join(parts, ", "))
		} else {
			fmt.Printf("%s: FAIL\n", path)
			worst = max(worst, 1)
		}
	}
	return worst
}

func peek(args []string) int {
	fs := flag.NewFlagSet("peek", flag.ExitOnError)
	full := fs.Bool("full", false, "for an archive, read the main file instead of the comment")
	fs.Parse(args)
	if fs.NArg() != 1 {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}
	path := fs.Arg(0)
	if strings.HasSuffix(strings.ToLower(path), ".zip") && !*full {
		if zr, err := zip.OpenReader(path); err == nil {
			comment := zr.Comment
			zr.Close()
			if comment != "" && utf8.ValidString(comment) {
				fmt.Println(strings.TrimRight(comment, "\n"))
				return 0
			}
		}
	}
	f, err := txtab.Load(path, false)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if !f.Doc.Recognized {
		fmt.Fprintf(os.Stderr, "%s: not a Text Tables file\n", path)
		return 2
	}
	if f.Doc.Error != nil {
		fmt.Fprintf(os.Stderr, "%s: %s\n", path, f.Doc.Error)
	}
	for _, s := range f.Doc.SystemLines {
		fmt.Println(s.Text)
	}
	if f.Doc.OK {
		return 0
	}
	return 1
}

func sql(args []string) int {
	fs := flag.NewFlagSet("sql", flag.ExitOnError)
	section := fs.String("section", "", "pre-data or post-data")
	strict := fs.Bool("strict", false, "skip statements whose version differs")
	fs.Parse(args)
	if fs.NArg() != 2 {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}
	f, err := txtab.Load(fs.Arg(1), false)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if !f.Doc.OK {
		fmt.Fprintf(os.Stderr, "%s: %v\n", fs.Arg(1), f.Doc.Error)
		return 1
	}
	lines, warnings := txtab.ApplySQL(f.Doc, fs.Arg(0), *strict, *section)
	for _, w := range warnings {
		fmt.Fprintf(os.Stderr, "%s: %s\n", fs.Arg(1), w)
	}
	for _, l := range lines {
		s := strings.TrimRight(l.Statement, " ")
		if !strings.HasSuffix(s, ";") {
			s += ";"
		}
		fmt.Println(s)
	}
	return 0
}

func describe(args []string) int {
	fs := flag.NewFlagSet("describe", flag.ExitOnError)
	output := fs.String("o", "", "write to this file")
	encoding := fs.String("encoding", "UTF-8", "encoding of the CSV when it has no BOM")
	fs.Parse(args)
	if fs.NArg() != 1 {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}
	data, err := os.ReadFile(fs.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	enc := *encoding
	if bom, n := txtab.SniffBOM(data); bom != "" {
		enc, data = bom, data[n:]
	}
	dec := txtab.DecoderFor(enc)
	if dec == nil {
		fmt.Fprintf(os.Stderr, "unknown encoding %s\n", enc)
		return 2
	}
	text, err := dec(data)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: does not decode as %s; pass --encoding\n", fs.Arg(0), enc)
		return 1
	}
	out, err := txtab.DescribeCSV(text, filepath.Base(fs.Arg(0)))
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", fs.Arg(0), err)
		return 1
	}
	if *output != "" {
		if err := os.WriteFile(*output, []byte(out), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		return 0
	}
	fmt.Print(out)
	return 0
}
