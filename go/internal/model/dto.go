package model

import (
	"encoding/json"
	"fmt"
	"math/big"
	"regexp"
	"strconv"
	"strings"
)

// SecretShareWidth is value(16) || mac(16).
const SecretShareWidth = 32

// ParseUUID is java.util.UUID.fromString (including the lenient legacy form); returns the
// canonical lowercase text.
func ParseUUID(s string) (string, bool) {
	if len(s) == 36 && s[8] == '-' && s[13] == '-' && s[18] == '-' && s[23] == '-' {
		ok := true
		for i := 0; i < 36; i++ {
			c := s[i]
			if i == 8 || i == 13 || i == 18 || i == 23 {
				continue
			}
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
				ok = false
				break
			}
		}
		if ok {
			return strings.ToLower(s), true
		}
	}
	parts := strings.Split(s, "-")
	if len(parts) != 5 || len(s) > 36 {
		return "", false
	}
	var v [5]int64
	for i, p := range parts {
		if p == "" || strings.ContainsAny(p, "+-") {
			return "", false
		}
		n, err := strconv.ParseInt(p, 16, 64)
		if err != nil {
			return "", false
		}
		v[i] = n
	}
	msb := v[0]
	msb <<= 16
	msb |= v[1]
	msb <<= 16
	msb |= v[2]
	lsb := v[3]
	lsb <<= 48
	lsb |= v[4]
	m, l := uint64(msb), uint64(lsb)
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", m>>32, (m>>16)&0xffff, m&0xffff, l>>48, l&0xffffffffffff), true
}

func parseUUIDField(raw json.RawMessage) (string, error) {
	s, isNil, err := jsonString(raw)
	if err != nil || isNil {
		return "", errBind
	}
	id, ok := ParseUUID(s)
	if !ok {
		return "", errBind
	}
	return id, nil
}

// Metadata is a secret id plus tags.
type Metadata struct {
	SecretID string
	Tags     []Tag // nil renders null
}

// JSON renders {"secretId":..,"tags":..}.
func (m Metadata) JSON() string {
	return `{"secretId":` + Quote(m.SecretID) + `,"tags":` + TagsJSON(m.Tags) + `}`
}

// SecretShare is Metadata plus data. Data == nil means absent.
type SecretShare struct {
	Metadata
	Data []byte
}

// JSON renders {"secretId","tags","data"}.
func (s SecretShare) JSON() string {
	return `{"secretId":` + Quote(s.SecretID) + `,"tags":` + TagsJSON(s.Tags) + `,"data":` + b64(s.Data) + `}`
}

// ParseSecretShare decodes a SecretShare request body.
func ParseSecretShare(b []byte) (*SecretShare, error) {
	m, err := parseObject(b)
	if err != nil {
		return nil, err
	}
	raw, ok := m["secretId"]
	if !ok {
		return nil, errBind
	}
	id, err := parseUUIDField(raw)
	if err != nil {
		return nil, err
	}
	s := &SecretShare{Metadata: Metadata{SecretID: id}}
	if raw, ok := m["tags"]; ok {
		tags, _, err := parseTagArray(raw)
		if err != nil {
			return nil, err
		}
		s.Tags = tags
	}
	if raw, ok := m["data"]; ok {
		d, isNil, err := Base64(raw)
		if err != nil || isNil {
			return nil, errBind
		}
		if len(d)%SecretShareWidth != 0 {
			return nil, errBind
		}
		s.Data = d
	}
	return s, nil
}

// MaskedInput is the POST /masked-inputs body. A nil entry in Data is a null value.
type MaskedInput struct {
	SecretID string
	Data     [][]byte // nil Data (not empty) means absent
	HasData  bool
	Tags     []Tag
}

// ParseMaskedInput decodes a MaskedInput request body.
func ParseMaskedInput(b []byte) (*MaskedInput, error) {
	m, err := parseObject(b)
	if err != nil {
		return nil, err
	}
	raw, ok := m["secretId"]
	if !ok {
		return nil, errBind
	}
	id, err := parseUUIDField(raw)
	if err != nil {
		return nil, err
	}
	mi := &MaskedInput{SecretID: id, Tags: []Tag{}}
	if raw, ok := m["data"]; ok && !isNull(raw) {
		var items []json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil {
			return nil, errBind
		}
		mi.HasData = true
		for _, it := range items {
			if isNull(it) {
				mi.Data = append(mi.Data, nil)
				continue
			}
			o, err := parseObject(it)
			if err != nil {
				return nil, err
			}
			v, ok := o["value"]
			if !ok {
				mi.Data = append(mi.Data, nil)
				continue
			}
			d, isNil, err := Base64(v)
			if err != nil {
				return nil, err
			}
			if isNil {
				d = nil
			} else if d == nil {
				d = []byte{}
			}
			mi.Data = append(mi.Data, d)
		}
	}
	if raw, ok := m["tags"]; ok {
		tags, isNil, err := parseTagArray(raw)
		if err != nil {
			return nil, err
		}
		if !isNil {
			mi.Tags = tags
		}
	}
	return mi, nil
}

// OutputDeliveryObject holds the five equally long share arrays.
type OutputDeliveryObject struct {
	SecretShares, RShares, VShares, WShares, UShares []byte
}

// JSON renders the ODO members in declaration order.
func (o OutputDeliveryObject) JSON() string { return "{" + o.members() + "}" }

func (o OutputDeliveryObject) members() string {
	return `"secretShares":` + b64(o.SecretShares) + `,"rShares":` + b64(o.RShares) + `,"vShares":` + b64(o.VShares) +
		`,"wShares":` + b64(o.WShares) + `,"uShares":` + b64(o.UShares)
}

// VerifiableSecretShareJSON is the flat merge of the secret share (metadata + data) and the ODO.
func VerifiableSecretShareJSON(s SecretShare, o OutputDeliveryObject) string {
	return `{"secretId":` + Quote(s.SecretID) + `,"tags":` + TagsJSON(s.Tags) + `,"data":` + b64(s.Data) + `,` + o.members() + `}`
}

// MetadataPage is the list endpoint response.
type MetadataPage struct {
	Content       []Metadata
	Number        int32
	Size          int32
	TotalElements int64
	TotalPages    int32
}

// JSON renders the page.
func (p MetadataPage) JSON() string {
	var b strings.Builder
	b.WriteString(`{"content":[`)
	for i, m := range p.Content {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(m.JSON())
	}
	fmt.Fprintf(&b, `],"number":%d,"size":%d,"totalElements":%d,"totalPages":%d}`, p.Number, p.Size, p.TotalElements, p.TotalPages)
	return b.String()
}

// FactorPair is a pair of big integers exchanged between players.
type FactorPair struct{ A, B *big.Int }

// ExchangeObject is the /inter-vcp/open body.
type ExchangeObject struct {
	OperationID string
	PlayerID    int32
	Values      []FactorPair
}

// JSON renders the exchange object with bare big numbers.
func (e ExchangeObject) JSON() string {
	var b strings.Builder
	b.WriteString(`{"operationId":` + Quote(e.OperationID) + `,"playerId":` + itoa(int64(e.PlayerID)) + `,"interimValues":`)
	if e.Values == nil {
		b.WriteString("null")
	} else {
		b.WriteByte('[')
		for i, p := range e.Values {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(`{"a":` + BigIntJSON(p.A) + `,"b":` + BigIntJSON(p.B) + `}`)
		}
		b.WriteByte(']')
	}
	b.WriteByte('}')
	return b.String()
}

func parseBigNumber(raw json.RawMessage) (*big.Int, error) {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return nil, errBind
	}
	if s[0] == '"' {
		var q string
		if json.Unmarshal(raw, &q) != nil {
			return nil, errBind
		}
		s = q
	}
	v, ok := new(big.Int).SetString(s, 10)
	if !ok {
		// Jackson accepts 1.0 / 1e2 for BigInteger as long as they are integral.
		f, ok := new(big.Float).SetPrec(2048).SetString(s)
		if !ok || !f.IsInt() {
			return nil, errBind
		}
		v, _ = f.Int(nil)
	}
	return v, nil
}

// ParseExchangeObject decodes the /inter-vcp/open body.
func ParseExchangeObject(b []byte) (*ExchangeObject, error) {
	m, err := parseObject(b)
	if err != nil {
		return nil, err
	}
	e := &ExchangeObject{}
	if raw, ok := m["operationId"]; ok && !isNull(raw) {
		if e.OperationID, err = parseUUIDField(raw); err != nil {
			return nil, err
		}
	}
	if raw, ok := m["playerId"]; ok && !isNull(raw) {
		s, _, err := jsonString(raw)
		if err != nil {
			return nil, err
		}
		n, err := strconv.ParseInt(s, 10, 32)
		if err != nil {
			return nil, errBind
		}
		e.PlayerID = int32(n)
	}
	if raw, ok := m["interimValues"]; ok && !isNull(raw) {
		var items []json.RawMessage
		if json.Unmarshal(raw, &items) != nil {
			return nil, errBind
		}
		e.Values = []FactorPair{}
		for _, it := range items {
			o, err := parseObject(it)
			if err != nil {
				return nil, err
			}
			var fp FactorPair
			if raw, ok := o["a"]; ok && !isNull(raw) {
				if fp.A, err = parseBigNumber(raw); err != nil {
					return nil, err
				}
			}
			if raw, ok := o["b"]; ok && !isNull(raw) {
				if fp.B, err = parseBigNumber(raw); err != nil {
					return nil, err
				}
			}
			e.Values = append(e.Values, fp)
		}
	}
	return e, nil
}

// TagFilterOperator of a filter.
type TagFilterOperator string

const (
	OpEquals      TagFilterOperator = ":"
	OpLessThan    TagFilterOperator = "<"
	OpGreaterThan TagFilterOperator = ">"
)

// TagFilter is one parsed filter criterion.
type TagFilter struct {
	Key, Value string
	Op         TagFilterOperator
}

var filterRe = regexp.MustCompile(`^([\w\-.%]+?)(:|<|>)([\w\-.%]+?)$`)

// URLDecode is java.net.URLDecoder.decode(s, "UTF-8").
func URLDecode(s string) (string, error) {
	if !strings.ContainsAny(s, "%+") {
		return s, nil
	}
	var out []byte
	for i := 0; i < len(s); {
		c := s[i]
		switch c {
		case '+':
			out = append(out, ' ')
			i++
		case '%':
			for i < len(s) && s[i] == '%' {
				if i+2 >= len(s) && len(s)-i < 3 {
					return "", fmt.Errorf("URLDecoder: Incomplete trailing escape (%%) pattern")
				}
				v, err := strconv.ParseUint(s[i+1:i+3], 16, 8)
				if err != nil || s[i+1] == '+' || s[i+1] == '-' {
					return "", fmt.Errorf("URLDecoder: Illegal hex characters in escape (%%) pattern - For input string: \"%s\"", s[i+1:i+3])
				}
				out = append(out, byte(v))
				i += 3
			}
		default:
			out = append(out, c)
			i++
		}
	}
	return strings.ToValidUTF8(string(out), "�"), nil
}

// ParseTagFilter mirrors TagFilter.fromString; errors are IllegalArgumentExceptions (400 with message).
func ParseTagFilter(s string) (TagFilter, error) {
	m := filterRe.FindStringSubmatch(s)
	if m == nil {
		return TagFilter{}, BadRequest("The given String cannot be parsed as a TagFilter: \"%s\".", s)
	}
	key, err := URLDecode(m[1])
	if err != nil {
		return TagFilter{}, BadRequest("%s", err.Error())
	}
	val, err := URLDecode(m[3])
	if err != nil {
		return TagFilter{}, BadRequest("%s", err.Error())
	}
	op := TagFilterOperator(m[2])
	if err := ValidateKey(key); err != nil {
		return TagFilter{}, BadRequest("%s", err.Error())
	}
	vt := TypeString
	if op != OpEquals {
		vt = TypeLong
	}
	if err := vt.ValidateValue(val); err != nil {
		return TagFilter{}, BadRequest("%s", err.Error())
	}
	return TagFilter{key, val, op}, nil
}

// ParseTagFilters splits like Java's String.split(",") (trailing empties dropped).
func ParseTagFilters(filter string) ([]TagFilter, error) {
	parts := strings.Split(filter, ",")
	for len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	var out []TagFilter
	for _, p := range parts {
		f, err := ParseTagFilter(p)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, nil
}
