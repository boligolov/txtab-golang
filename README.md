# txtab-golang

Go library and command line for **Text Tables** (`.txtab`): plain text tables that describe themselves. CSV rows, plus system lines for types, units, totals, sources and relations.

- Specification and guide: [texttables.dev](https://texttables.dev), source in [boligolov/txtab](https://github.com/boligolov/txtab)
- Text Tables 0.1 (draft). Standard library only. Go 1.22+.
- Conformance: passes all 204 fixtures of the shared corpus, `.txtab.zip` with Store, Deflate, BZIP2 and LZMA included.

## Install

```sh
go get github.com/boligolov/txtab-golang
go install github.com/boligolov/txtab-golang/cmd/txtab@latest
```

## Use

```go
import "github.com/boligolov/txtab-golang"

f, err := txtab.Load("shop.txtab", true) // .txtab or .txtab.zip; true opens reference= files and column directories
if err != nil { … }
if !f.OK() {
    fmt.Println(f.Doc.Error) // the first FAIL, with its line
}
for _, d := range f.Diagnostics() { // warnings (and the error), in file order
    fmt.Println(d)
}

orders := f.Doc.Table("orders")
fmt.Println(orders.Rows, orders.RowsRead, orders.Meta["grain"], orders.Aggregates["sum"])
for _, c := range orders.Columns {
    fmt.Println(c.Name, c.Type, c.Attrs["unit"])
}
fmt.Println(orders.Data.Header, orders.Data.Records[:2])
```

Without the file system:

```go
doc := txtab.Parse(data)                                  // *Doc: Recognized, OK, Error, Diagnostics, Tables, …
lines, warnings := txtab.ApplySQL(doc, "postgres-17", false, "") // pre-data, then post-data, in file order
text, err := txtab.DescribeCSV(csvText, "orders.csv")     // plain CSV → a .txtab that describes it
```

Values stay strings as written (after CSV unquoting). Nulls are empty fields or the table's `null=` marker: `table.IsNull(v)`. The parser never converts types or substitutes `default=`, as the spec requires.

Encodings without a BOM: UTF-8, ISO-8859-1, ISO-8859-15, Windows-1251, Windows-1252 and US-ASCII are built in; any other `encoding=` is reported as `encoding-unknown` and read as UTF-8.

## Command line

```sh
txtab verify FILE...              # errors and warnings; rows-mismatch and columns-mismatch fail; exit 1 on failure
txtab peek [--full] FILE          # the system lines: the schema without the data (an archive: its ZIP comment)
txtab sql [--section S] [--strict] DIALECT FILE   # SQL for a database, in execution order
txtab describe [-o OUT] FILE.csv  # describe a plain CSV/TSV as Text Tables
```

## Tests

The tests run the shared corpus in `texttables/fixtures` (a sibling checkout of [boligolov/txtab](https://github.com/boligolov/txtab)), or `$TXTAB_FIXTURES`. They read `fixtures.json`, the JSON mirror of `fixtures.yaml`, so no YAML library is needed.

```sh
go test ./...
go test -run 'TestFixtures/archive/' -v .
```

## Layout

| File | What |
| --- | --- |
| `parse.go` | bytes → `*Doc`: recognition, encodings, first line, blocks, records by count with recovery, columns, aggregates, notes, links, charts, SQL; the first FAIL stops it |
| `open.go` | Opens `reference=` files and `storage=columns` directories through a `Source` |
| `archive.go`, `lzma.go` | `.txtab.zip`: the main file, entries, the ZIP comment; LZMA decoding (archive/zip has the rest) |
| `sql.go` | Statements for a target dialect |
| `describe.go` | CSV → `.txtab` |
| `errors.go` | The error-code registry of `spec/errors.md` |
| `text.go` | Tokenizer, CSV records, encodings |
| `cmd/txtab` | The command line |

A Python implementation is [txtab-python](https://github.com/boligolov/txtab-python); a TypeScript one lives in the specification repository (`website/src/lib/txtab`, used by the playground). They follow the same structure.

## License

MIT
