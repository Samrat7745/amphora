// Package jwt extracts the user id claim like the Java JwtReader: no signature, exp, iss or aud
// validation, STANDARD base64 only (a URL-safe token yields the Java IllegalArgumentException).
package jwt

import (
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/carbynestack/amphora-go/internal/model"
)

// Reader reads the configured (possibly dotted) claim.
type Reader struct{ Field string }

func noToken() error { return model.Unauthorized("No token provided") }

// UserID extracts the claim from an Authorization header value.
func (r Reader) UserID(header string) (string, error) {
	if !strings.HasPrefix(header, "Bearer ") {
		return "", noToken()
	}
	token := header[7:]
	parts := javaSplitDot(token)
	if len(parts) != 3 {
		return "", noToken()
	}
	payload, err := decodeStd(parts[1])
	if err != nil {
		return "", err
	}
	var node any
	dec := json.NewDecoder(strings.NewReader(payload))
	dec.UseNumber()
	if err := dec.Decode(&node); err != nil {
		return "", noToken()
	}
	cur := node
	for _, f := range javaSplitDot(r.Field) {
		obj, ok := cur.(map[string]any)
		if !ok {
			// JsonNode.get(String) on non-object nodes returns null
			return "", noToken()
		}
		cur, ok = obj[f]
		if !ok {
			return "", noToken()
		}
	}
	return asText(cur), nil
}

// javaSplitDot is "x".split("\\.") (trailing empty strings removed).
func javaSplitDot(s string) []string {
	p := strings.Split(s, ".")
	for len(p) > 0 && p[len(p)-1] == "" {
		p = p[:len(p)-1]
	}
	if s == "" {
		return []string{""}
	}
	return p
}

// asText is JsonNode.asText(): containers and null render as "" / "null".
func asText(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case string:
		return x
	case bool:
		if x {
			return "true"
		}
		return "false"
	case json.Number:
		return x.String()
	}
	return ""
}

// decodeStd is java.util.Base64.getDecoder().decode (padding optional, strict alphabet).
func decodeStd(s string) (string, error) {
	for i := 0; i < len(s); i++ {
		c := s[i]
		ok := c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '+' || c == '/' || c == '='
		if !ok {
			return "", model.BadRequest("Illegal base64 character %x", c)
		}
	}
	b, err := base64.RawStdEncoding.DecodeString(strings.TrimRight(s, "="))
	if err != nil {
		b, err = base64.StdEncoding.DecodeString(s)
		if err != nil {
			return "", model.BadRequest("Input byte array has incorrect ending byte at %d", len(s))
		}
	}
	return string(b), nil
}
