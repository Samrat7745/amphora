package model

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// Quote renders s as a JSON string exactly like Jackson's default writer (no HTML escaping,
// uppercase \u00XX for control characters).
func Quote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"':
			b.WriteString(`\"`)
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\b':
			b.WriteString(`\b`)
		case r == '\f':
			b.WriteString(`\f`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20:
			fmt.Fprintf(&b, `\u%04X`, r)
		case r == utf8.RuneError:
			b.WriteString("�")
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// Utf16Len is Java's String.length().
func Utf16Len(s string) int {
	n := 0
	for _, r := range s {
		n += len(utf16.Encode([]rune{r}))
	}
	return n
}

var errBind = Binding("unreadable JSON")

func isNull(raw json.RawMessage) bool { return bytes.Equal(bytes.TrimSpace(raw), []byte("null")) }

// parseObject decodes a JSON object with exact (case-sensitive) member names.
func parseObject(b []byte) (map[string]json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	var m map[string]json.RawMessage
	if err := dec.Decode(&m); err != nil || m == nil {
		return nil, errBind
	}
	if _, err := dec.Token(); err == nil {
		return nil, errBind // trailing content
	}
	return m, nil
}

// jsonString reads a Jackson-style String (scalars are coerced to text, null yields ok=false).
func jsonString(raw json.RawMessage) (s string, isNil bool, err error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return "", false, errBind
	}
	switch raw[0] {
	case 'n':
		return "", true, nil
	case '"':
		if err := json.Unmarshal(raw, &s); err != nil {
			return "", false, errBind
		}
		return s, false, nil
	case 't', 'f', '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		return string(raw), false, nil
	}
	return "", false, errBind
}

// Base64 decodes standard base64 (padding optional).
func Base64(raw json.RawMessage) ([]byte, bool, error) {
	s, isNil, err := jsonString(raw)
	if err != nil || isNil {
		return nil, isNil, err
	}
	out, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		out, err = base64.RawStdEncoding.DecodeString(s)
		if err != nil {
			return nil, false, errBind
		}
	}
	return out, false, nil
}

func b64(b []byte) string { return `"` + base64.StdEncoding.EncodeToString(b) + `"` }

// BigIntJSON renders big integers as bare numbers (Jackson BigInteger).
func BigIntJSON(v *big.Int) string { return v.String() }

func itoa(i int64) string { return strconv.FormatInt(i, 10) }
