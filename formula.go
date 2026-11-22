package txtab

import (
	"fmt"
	"regexp"
	"strings"
)

// The portable formula language of columns.md#formula-language: parse only, to validate and find references.

var formulaFunctions = map[string]bool{
	"abs": true, "round": true, "floor": true, "ceil": true, "coalesce": true, "nullif": true, "least": true,
	"greatest": true, "length": true, "lower": true, "upper": true, "trim": true, "substr": true, "concat": true,
}
var formulaWords = map[string]bool{
	"and": true, "or": true, "not": true, "case": true, "when": true, "then": true, "else": true, "end": true,
	"true": true, "false": true, "null": true,
}

var (
	reNum = regexp.MustCompile(`^(?:\d+\.?\d*|\.\d+)(?:[eE][+-]?\d+)?`)
	reID  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*`)
)

type ftok struct{ k, v string }

func lexFormula(src string) ([]ftok, error) {
	var out []ftok
	i := 0
	for i < len(src) {
		c := src[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case (c >= '0' && c <= '9') || c == '.':
			m := reNum.FindString(src[i:])
			if m == "" {
				return nil, fmt.Errorf("bad number at %d", i)
			}
			out = append(out, ftok{"num", m})
			i += len(m)
		case c == '\'':
			j := i + 1
			var s strings.Builder
			for {
				if j >= len(src) {
					return nil, fmt.Errorf("unterminated string")
				}
				if src[j] == '\'' {
					if j+1 < len(src) && src[j+1] == '\'' {
						s.WriteByte('\'')
						j += 2
						continue
					}
					break
				}
				s.WriteByte(src[j])
				j++
			}
			out = append(out, ftok{"str", s.String()})
			i = j + 1
		case (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || c == '_':
			m := reID.FindString(src[i:])
			if low := strings.ToLower(m); formulaWords[low] {
				out = append(out, ftok{"word", low})
			} else {
				out = append(out, ftok{"id", m})
			}
			i += len(m)
		default:
			if i+1 < len(src) {
				if two := src[i : i+2]; two == "<>" || two == "<=" || two == ">=" {
					out = append(out, ftok{"op", two})
					i += 2
					continue
				}
			}
			if strings.IndexByte("+-*/%=<>(),", c) >= 0 {
				out = append(out, ftok{"op", string(c)})
				i++
				continue
			}
			return nil, fmt.Errorf("unexpected %q", string(c))
		}
	}
	return append(out, ftok{"end", ""}), nil
}

type formulaParser struct {
	t    []ftok
	p    int
	refs []string
}

func (f *formulaParser) is(k, v string) bool { return f.t[f.p].k == k && f.t[f.p].v == v }

func (f *formulaParser) expect(k, v string) {
	if !f.is(k, v) {
		panic(fmt.Errorf("expected %q", v))
	}
	f.p++
}

func (f *formulaParser) expr() {
	f.and()
	for f.is("word", "or") {
		f.p++
		f.and()
	}
}

func (f *formulaParser) and() {
	f.not()
	for f.is("word", "and") {
		f.p++
		f.not()
	}
}

func (f *formulaParser) not() {
	if f.is("word", "not") {
		f.p++
		f.not()
		return
	}
	f.cmp()
}

func (f *formulaParser) cmp() {
	f.add()
	if t := f.t[f.p]; t.k == "op" && (t.v == "=" || t.v == "<>" || t.v == "<" || t.v == "<=" || t.v == ">" || t.v == ">=") {
		f.p++
		f.add()
	}
}

func (f *formulaParser) add() {
	f.mul()
	for f.is("op", "+") || f.is("op", "-") {
		f.p++
		f.mul()
	}
}

func (f *formulaParser) mul() {
	f.unary()
	for f.is("op", "*") || f.is("op", "/") || f.is("op", "%") {
		f.p++
		f.unary()
	}
}

func (f *formulaParser) unary() {
	if f.is("op", "-") {
		f.p++
		f.unary()
		return
	}
	f.primary()
}

func (f *formulaParser) primary() {
	t := f.t[f.p]
	switch {
	case t.k == "num" || t.k == "str" || (t.k == "word" && (t.v == "true" || t.v == "false" || t.v == "null")):
		f.p++
	case t.k == "word" && t.v == "case":
		f.p++
		if !f.is("word", "when") {
			panic(fmt.Errorf("case without when"))
		}
		for f.is("word", "when") {
			f.p++
			f.expr()
			f.expect("word", "then")
			f.expr()
		}
		if f.is("word", "else") {
			f.p++
			f.expr()
		}
		f.expect("word", "end")
	case t.k == "id":
		f.p++
		if f.is("op", "(") {
			if !formulaFunctions[strings.ToLower(t.v)] {
				panic(fmt.Errorf("unknown function %s", t.v))
			}
			f.p++
			if !f.is("op", ")") {
				f.expr()
				for f.is("op", ",") {
					f.p++
					f.expr()
				}
			}
			f.expect("op", ")")
			return
		}
		for _, r := range f.refs {
			if r == t.v {
				return
			}
		}
		f.refs = append(f.refs, t.v)
	case f.is("op", "("):
		f.p++
		f.expr()
		f.expect("op", ")")
	case t.k == "end":
		panic(fmt.Errorf("unexpected end"))
	default:
		panic(fmt.Errorf("unexpected %q", t.v))
	}
}

// ParseFormula parses a formula and returns the column names it reads.
func ParseFormula(src string) (refs []string, err error) {
	toks, err := lexFormula(src)
	if err != nil {
		return nil, err
	}
	f := &formulaParser{t: toks}
	defer func() {
		if r := recover(); r != nil {
			e, ok := r.(error)
			if !ok {
				panic(r)
			}
			refs, err = nil, e
		}
	}()
	f.expr()
	if f.t[f.p].k != "end" {
		return nil, fmt.Errorf("unexpected %q", f.t[f.p].v)
	}
	return f.refs, nil
}
