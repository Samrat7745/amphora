package model

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
)

// TagValueType is STRING or LONG.
type TagValueType string

const (
	TypeString TagValueType = "STRING"
	TypeLong   TagValueType = "LONG"

	MaxKeyLength   = 128
	MaxValueLength = 256

	InvalidKeyStringMsg = "A tag's key must be an alphanumeric string with a minimum length of 1. It may contain" +
		" periods (.), hyphens (-) and underscores (_)."
	InvalidKeyLengthMsg = "The length of a tag's key cannot be longer then 128 characters."
)

var keyRe = regexp.MustCompile(`^[-.\w]+$`)

// Tag is a key/value pair attached to a secret.
type Tag struct {
	Key       string
	Value     string
	ValueType TagValueType
}

// String mimics Lombok's toString.
func (t Tag) String() string {
	return fmt.Sprintf("Tag(key=%s, value=%s, valueType=%s)", t.Key, t.Value, t.ValueType)
}

// ValidateKey mirrors Tag.validateKey.
func ValidateKey(k string) error {
	if Utf16Len(k) > MaxKeyLength {
		return fmt.Errorf("%s", InvalidKeyLengthMsg)
	}
	if !keyRe.MatchString(k) {
		return fmt.Errorf("%s", InvalidKeyStringMsg)
	}
	return nil
}

// ValidateValue mirrors TagValueType.accept.
func (vt TagValueType) ValidateValue(v string) error {
	if Utf16Len(v) > MaxValueLength {
		return fmt.Errorf("The given value exceeds the maximum length of %d: (%d) \"%s\"", MaxValueLength, Utf16Len(v), v)
	}
	if vt == TypeLong {
		if _, err := strconv.ParseInt(v, 10, 64); err != nil {
			return fmt.Errorf("Value cannot be parsed as long.")
		}
	}
	return nil
}

// NewTag validates and builds a tag.
func NewTag(key, value string, vt TagValueType) (Tag, error) {
	if err := ValidateKey(key); err != nil {
		return Tag{}, err
	}
	if err := vt.ValidateValue(value); err != nil {
		return Tag{}, err
	}
	return Tag{key, value, vt}, nil
}

// JSON renders the tag like Jackson.
func (t Tag) JSON() string {
	return `{"key":` + Quote(t.Key) + `,"value":` + Quote(t.Value) + `,"valueType":` + Quote(string(t.ValueType)) + `}`
}

// TagsJSON renders a tag list; nil renders null.
func TagsJSON(tags []Tag) string {
	if tags == nil {
		return "null"
	}
	b := []byte{'['}
	for i, t := range tags {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, t.JSON()...)
	}
	return string(append(b, ']'))
}

func parseTagObject(m map[string]json.RawMessage) (Tag, error) {
	kraw, ok := m["key"]
	if !ok {
		return Tag{}, errBind
	}
	key, isNil, err := jsonString(kraw)
	if err != nil || isNil {
		return Tag{}, errBind
	}
	value := ""
	if raw, ok := m["value"]; ok {
		v, isNil, err := jsonString(raw)
		if err != nil || isNil {
			return Tag{}, errBind
		}
		value = v
	}
	vt := TypeString
	if raw, ok := m["valueType"]; ok {
		s, isNil, err := jsonString(raw)
		if err != nil || isNil || (s != "STRING" && s != "LONG") {
			return Tag{}, errBind
		}
		vt = TagValueType(s)
	}
	t, err := NewTag(key, value, vt)
	if err != nil {
		return Tag{}, errBind
	}
	return t, nil
}

// ParseTag decodes a Tag request body. All failures are Spring binding errors (400, empty body).
func ParseTag(b []byte) (Tag, error) {
	m, err := parseObject(b)
	if err != nil {
		return Tag{}, err
	}
	return parseTagObject(m)
}

// ParseTagArray decodes a JSON array of tags (null elements are rejected as binding errors).
func parseTagArray(raw json.RawMessage) ([]Tag, bool, error) {
	if isNull(raw) {
		return nil, true, nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, false, errBind
	}
	out := make([]Tag, 0, len(items))
	for _, it := range items {
		if isNull(it) {
			continue // Metadata builder removes nulls
		}
		m, err := parseObject(it)
		if err != nil {
			return nil, false, err
		}
		t, err := parseTagObject(m)
		if err != nil {
			return nil, false, err
		}
		out = append(out, t)
	}
	return out, false, nil
}

// ParseTagList decodes the body of PUT /tags (List<Tag>). Returns nil slice for JSON null.
func ParseTagList(b []byte) ([]Tag, error) {
	tags, _, err := parseTagArray(json.RawMessage(b))
	return tags, err
}
