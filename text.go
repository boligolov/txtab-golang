package txtab

import (
	"encoding/binary"
	"errors"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// ---------------------------------------------------------------- key=value

type kvPair struct{ Key, Value string }

// tokenizeKV implements first-line.md#tokens.
func tokenizeKV(s string) ([]kvPair, string) {
	var pairs []kvPair
	n := len(s)
	i := 0
	for i < n {
		for i < n && s[i] == ' ' {
			i++
		}
		if i >= n {
			break
		}
		j := i
		for j < n && s[j] != '=' && s[j] != ' ' {
			j++
		}
		if j >= n || s[j] != '=' {
			return pairs, `token "` + s[i:j] + `" has no "="`
		}
		key := s[i:j]
		if key == "" {
			return pairs, "empty key"
		}
		j++
		var value string
		if j < n && s[j] == '"' {
			j++
			var b strings.Builder
			for {
				if j >= n {
					return pairs, "unterminated quote in " + key + "="
				}
				if s[j] == '"' {
					if j+1 < n && s[j+1] == '"' {
						b.WriteByte('"')
						j += 2
						continue
					}
					j++
					break
				}
				b.WriteByte(s[j])
				j++
			}
			if j < n && s[j] != ' ' {
				return pairs, "text after the closing quote of " + key + "="
			}
			value = b.String()
		} else {
			k := j
			for k < n && s[k] != ' ' {
				k++
			}
			value = s[j:k]
			j = k
		}
		pairs = append(pairs, kvPair{key, value})
		i = j
	}
	return pairs, ""
}

// ---------------------------------------------------------------- dialect

var delimNames = map[string]string{"comma": ",", "tab": "\t", "pipe": "|", "semicolon": ";"}

// DelimChars resolves a delim= value: a name, else the literal string.
func DelimChars(v string) string {
	if d, ok := delimNames[v]; ok {
		return d
	}
	return v
}

// QuoteChar resolves a quote= value; "" means no quoting.
func QuoteChar(v string) string {
	switch v {
	case "none":
		return ""
	case "double":
		return `"`
	case "single":
		return "'"
	}
	return v
}

var errUnterminated = errors.New("unterminated quote")

// SplitRecord splits one single-line record. A quote still open at the end of the line returns errUnterminated.
func SplitRecord(line, delim, quote string) ([]string, error) {
	if quote == "" {
		return strings.Split(line, delim), nil
	}
	var fields []string
	n := len(line)
	i := 0
	for {
		if strings.HasPrefix(line[i:], quote) {
			i += len(quote)
			var b strings.Builder
			for {
				if i >= n {
					return nil, errUnterminated
				}
				if strings.HasPrefix(line[i:], quote) {
					if strings.HasPrefix(line[i+len(quote):], quote) {
						b.WriteString(quote)
						i += 2 * len(quote)
						continue
					}
					i += len(quote)
					break
				}
				b.WriteByte(line[i])
				i++
			}
			next := strings.Index(line[i:], delim)
			if next < 0 {
				b.WriteString(line[i:])
				fields = append(fields, b.String())
				break
			}
			b.WriteString(line[i : i+next])
			fields = append(fields, b.String())
			i += next + len(delim)
		} else {
			next := strings.Index(line[i:], delim)
			if next < 0 {
				fields = append(fields, line[i:])
				break
			}
			fields = append(fields, line[i:i+next])
			i += next + len(delim)
		}
	}
	return fields, nil
}

// ---------------------------------------------------------------- encodings

// SniffBOM returns the encoding named by a BOM and its length (file-structure.md#encoding).
func SniffBOM(b []byte) (string, int) {
	switch {
	case len(b) >= 4 && b[0] == 0 && b[1] == 0 && b[2] == 0xfe && b[3] == 0xff:
		return "UTF-32BE", 4
	case len(b) >= 4 && b[0] == 0xff && b[1] == 0xfe && b[2] == 0 && b[3] == 0:
		return "UTF-32LE", 4
	case len(b) >= 2 && b[0] == 0xfe && b[1] == 0xff:
		return "UTF-16BE", 2
	case len(b) >= 2 && b[0] == 0xff && b[1] == 0xfe:
		return "UTF-16LE", 2
	case len(b) >= 3 && b[0] == 0xef && b[1] == 0xbb && b[2] == 0xbf:
		return "UTF-8", 3
	}
	return "", 0
}

func encodingKey(name string) string {
	return strings.NewReplacer("-", "", "_", "").Replace(strings.ToLower(name))
}

var wide = map[string]bool{"utf16": true, "utf16le": true, "utf16be": true, "utf32": true, "utf32le": true, "utf32be": true, "ucs2": true, "ucs4": true}

func isASCIICompatible(name string) bool { return !wide[encodingKey(name)] }

// Decoder turns bytes into a UTF-8 string, failing on bytes that do not decode.
type Decoder func([]byte) (string, error)

var errInvalidBytes = errors.New("invalid bytes")

var canonicalEncodings = map[string]string{
	"utf8": "utf8", "utf16": "utf16le", "utf16le": "utf16le", "utf16be": "utf16be",
	"utf32": "utf32le", "utf32le": "utf32le", "utf32be": "utf32be",
	"iso88591": "latin1", "latin1": "latin1", "l1": "latin1", "iso885915": "latin9",
	"windows1251": "cp1251", "cp1251": "cp1251", "windows1252": "cp1252", "cp1252": "cp1252",
	"usascii": "ascii", "ascii": "ascii",
}

func sameEncoding(a, b string) bool {
	ca, oka := canonicalEncodings[encodingKey(a)]
	cb, okb := canonicalEncodings[encodingKey(b)]
	if oka && okb {
		return ca == cb
	}
	return encodingKey(a) == encodingKey(b)
}

// DecoderFor returns a strict decoder, or nil for an encoding this implementation does not know.
// Known: UTF-8, UTF-16LE/BE, UTF-32LE/BE, ISO-8859-1, ISO-8859-15, Windows-1251, Windows-1252, US-ASCII.
func DecoderFor(name string) Decoder {
	switch canonicalEncodings[encodingKey(name)] {
	case "utf8":
		return func(b []byte) (string, error) {
			if !utf8.Valid(b) {
				return "", errInvalidBytes
			}
			return string(b), nil
		}
	case "utf16le":
		return func(b []byte) (string, error) { return decodeUTF16(b, binary.LittleEndian) }
	case "utf16be":
		return func(b []byte) (string, error) { return decodeUTF16(b, binary.BigEndian) }
	case "utf32le":
		return func(b []byte) (string, error) { return decodeUTF32(b, binary.LittleEndian) }
	case "utf32be":
		return func(b []byte) (string, error) { return decodeUTF32(b, binary.BigEndian) }
	case "latin1":
		return func(b []byte) (string, error) { return decodeTable(b, nil), nil }
	case "latin9":
		return func(b []byte) (string, error) { return decodeTable(b, latin9), nil }
	case "cp1251":
		return func(b []byte) (string, error) { return decodeStrictTable(b, cp1251) }
	case "cp1252":
		return func(b []byte) (string, error) { return decodeStrictTable(b, cp1252) }
	case "ascii":
		return func(b []byte) (string, error) {
			for _, c := range b {
				if c > 0x7f {
					return "", errInvalidBytes
				}
			}
			return string(b), nil
		}
	}
	return nil
}

func decodeUTF16(b []byte, order binary.ByteOrder) (string, error) {
	if len(b)%2 != 0 {
		return "", errInvalidBytes
	}
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = order.Uint16(b[2*i:])
	}
	for i := 0; i < len(u); i++ {
		switch {
		case u[i] >= 0xd800 && u[i] < 0xdc00:
			if i+1 >= len(u) || u[i+1] < 0xdc00 || u[i+1] > 0xdfff {
				return "", errInvalidBytes
			}
			i++
		case u[i] >= 0xdc00 && u[i] <= 0xdfff:
			return "", errInvalidBytes
		}
	}
	return string(utf16.Decode(u)), nil
}

func decodeUTF32(b []byte, order binary.ByteOrder) (string, error) {
	if len(b)%4 != 0 {
		return "", errInvalidBytes
	}
	var s strings.Builder
	for i := 0; i < len(b); i += 4 {
		cp := order.Uint32(b[i:])
		if cp > 0x10ffff || (cp >= 0xd800 && cp <= 0xdfff) {
			return "", errInvalidBytes
		}
		s.WriteRune(rune(cp))
	}
	return s.String(), nil
}

// decodeTable maps 0x80–0xFF through a 128-entry table (nil: ISO-8859-1).
func decodeTable(b []byte, table *[128]rune) string {
	var s strings.Builder
	for _, c := range b {
		switch {
		case c < 0x80:
			s.WriteByte(c)
		case table == nil:
			s.WriteRune(rune(c))
		default:
			s.WriteRune(table[c-0x80])
		}
	}
	return s.String()
}

func decodeStrictTable(b []byte, table *[128]rune) (string, error) {
	for _, c := range b {
		if c >= 0x80 && table[c-0x80] == 0 {
			return "", errInvalidBytes
		}
	}
	return decodeTable(b, table), nil
}

func latin1(b []byte) string { return decodeTable(b, nil) }

var cp1251 = &[128]rune{
	0x0402, 0x0403, 0x201A, 0x0453, 0x201E, 0x2026, 0x2020, 0x2021, 0x20AC, 0x2030, 0x0409, 0x2039, 0x040A, 0x040C, 0x040B, 0x040F,
	0x0452, 0x2018, 0x2019, 0x201C, 0x201D, 0x2022, 0x2013, 0x2014, 0, 0x2122, 0x0459, 0x203A, 0x045A, 0x045C, 0x045B, 0x045F,
	0x00A0, 0x040E, 0x045E, 0x0408, 0x00A4, 0x0490, 0x00A6, 0x00A7, 0x0401, 0x00A9, 0x0404, 0x00AB, 0x00AC, 0x00AD, 0x00AE, 0x0407,
	0x00B0, 0x00B1, 0x0406, 0x0456, 0x0491, 0x00B5, 0x00B6, 0x00B7, 0x0451, 0x2116, 0x0454, 0x00BB, 0x0458, 0x0405, 0x0455, 0x0457,
	0x0410, 0x0411, 0x0412, 0x0413, 0x0414, 0x0415, 0x0416, 0x0417, 0x0418, 0x0419, 0x041A, 0x041B, 0x041C, 0x041D, 0x041E, 0x041F,
	0x0420, 0x0421, 0x0422, 0x0423, 0x0424, 0x0425, 0x0426, 0x0427, 0x0428, 0x0429, 0x042A, 0x042B, 0x042C, 0x042D, 0x042E, 0x042F,
	0x0430, 0x0431, 0x0432, 0x0433, 0x0434, 0x0435, 0x0436, 0x0437, 0x0438, 0x0439, 0x043A, 0x043B, 0x043C, 0x043D, 0x043E, 0x043F,
	0x0440, 0x0441, 0x0442, 0x0443, 0x0444, 0x0445, 0x0446, 0x0447, 0x0448, 0x0449, 0x044A, 0x044B, 0x044C, 0x044D, 0x044E, 0x044F,
}

var cp1252 = func() *[128]rune {
	t := [128]rune{
		0x20AC, 0, 0x201A, 0x0192, 0x201E, 0x2026, 0x2020, 0x2021, 0x02C6, 0x2030, 0x0160, 0x2039, 0x0152, 0, 0x017D, 0,
		0, 0x2018, 0x2019, 0x201C, 0x201D, 0x2022, 0x2013, 0x2014, 0x02DC, 0x2122, 0x0161, 0x203A, 0x0153, 0, 0x017E, 0x0178,
	}
	for i := 0x20; i < 0x80; i++ {
		t[i] = rune(0x80 + i)
	}
	return &t
}()

var latin9 = func() *[128]rune {
	var t [128]rune
	for i := range t {
		t[i] = rune(0x80 + i)
	}
	for k, v := range map[int]rune{0xA4: 0x20AC, 0xA6: 0x0160, 0xA8: 0x0161, 0xB4: 0x017D, 0xB8: 0x017E, 0xBC: 0x0152, 0xBD: 0x0153, 0xBE: 0x0178} {
		t[k-0x80] = v
	}
	return &t
}()
