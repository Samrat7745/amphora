package model

import (
	"os"
	"strings"
	"testing"
)

func TestParseUUID(t *testing.T) {
	cases := map[string]string{
		"82A73814-321C-4261-ABCD-27C6C0EBFB26": "82a73814-321c-4261-abcd-27c6c0ebfb26",
		"1-2-3-4-5":                            "00000001-0002-0003-0004-000000000005",
	}
	for in, want := range cases {
		if got, ok := ParseUUID(in); !ok || got != want {
			t.Errorf("%s: %s %v", in, got, ok)
		}
	}
	for _, bad := range []string{"", "x", "1-2-3-4", "g-2-3-4-5", "1-2-3-4-5-6", "-1-2-3-4-5", "82a73814-321c-4261-abcd-27c6c0ebfb2z"} {
		if _, ok := ParseUUID(bad); ok {
			t.Errorf("%q must be rejected", bad)
		}
	}
}

func TestQuoteMatchesJackson(t *testing.T) {
	got := Quote("a\"b\\c\n\t\x01<>&/é ")
	want := "\"a\\\"b\\\\c\\n\\t\\u0001<>&/é \""
	if got != want {
		t.Fatalf("%s != %s", got, want)
	}
}

func TestTagValidation(t *testing.T) {
	ok := []string{`{"key":"a"}`, `{"key":"a.b-c_d","value":"x","valueType":"STRING"}`, `{"key":"n","value":"-5","valueType":"LONG"}`, `{"key":"n","value":"+5","valueType":"LONG"}`, `{"key":"k","value":5}`}
	for _, b := range ok {
		if _, err := ParseTag([]byte(b)); err != nil {
			t.Errorf("%s: %v", b, err)
		}
	}
	bad := []string{`{}`, `{"key":null}`, `{"key":"a b"}`, `{"key":""}`, `{"key":"` + strings.Repeat("a", 129) + `"}`, `{"key":"a","value":null}`,
		`{"key":"a","valueType":"X"}`, `{"key":"a","value":"x","valueType":"LONG"}`, `{"key":"a","value":" 5","valueType":"LONG"}`,
		`{"key":"a","value":"` + strings.Repeat("v", 257) + `"}`, `[]`, `null`, ``, `{"KEY":"a"}`}
	for _, b := range bad {
		if _, err := ParseTag([]byte(b)); err == nil {
			t.Errorf("%s must be rejected", b)
		}
	}
	if _, err := ParseTag([]byte(`{"key":"é"}`)); err == nil {
		t.Error("non-ascii key must be rejected (\\w is ASCII in Java)")
	}
	tg, _ := ParseTag([]byte(`{"key":"a"}`))
	if tg.JSON() != `{"key":"a","value":"","valueType":"STRING"}` || tg.String() != "Tag(key=a, value=, valueType=STRING)" {
		t.Fatal(tg.JSON(), tg.String())
	}
}

func TestSecretShareAndMaskedInput(t *testing.T) {
	d := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=" // 43 bytes
	if _, err := ParseSecretShare([]byte(`{"secretId":"82a73814-321c-4261-abcd-27c6c0ebfb26","data":"` + d + `"}`)); err == nil {
		t.Error("data length not multiple of 32")
	}
	s, err := ParseSecretShare([]byte(`{"secretId":"82a73814-321c-4261-abcd-27c6c0ebfb26","data":"` + strings32() + `","tags":[null,{"key":"a"}]}`))
	if err != nil || len(s.Data) != 32 || len(s.Tags) != 1 {
		t.Fatal(err, s)
	}
	mi, err := ParseMaskedInput([]byte(`{"secretId":"82a73814-321c-4261-abcd-27c6c0ebfb26","data":[{"value":"AAAAAAAAAAAAAAAAAAAAAA=="}]}`))
	if err != nil || !mi.HasData || len(mi.Data) != 1 || len(mi.Data[0]) != 16 || mi.Tags == nil {
		t.Fatal(err, mi)
	}
	if _, err := ParseMaskedInput([]byte(`{"data":[]}`)); err == nil {
		t.Error("secretId required")
	}
}

func strings32() string { return "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=" }

func TestExchangeObjectJSONMatchesJackson(t *testing.T) {
	want, err := os.ReadFile("testdata/meo.json")
	if err != nil {
		t.Fatal(err)
	}
	e, err := ParseExchangeObject(want)
	if err != nil {
		t.Fatal(err)
	}
	if got := e.JSON(); got != string(want) {
		t.Fatalf("\n got %s\nwant %s", got, want)
	}
	if _, err := ParseExchangeObject([]byte(`{"operationId":"x"}`)); err == nil {
		t.Error("bad uuid")
	}
}

func TestTagFilters(t *testing.T) {
	fs, err := ParseTagFilters("a:b,n%3E5,k<7,")
	if err != nil {
		// '>' and '<' must come unescaped in the (already decoded) value
		t.Log(err)
	}
	fs, err = ParseTagFilters("a:b,n>5,k<7,")
	if err != nil || len(fs) != 3 || fs[1].Op != OpGreaterThan || fs[2].Value != "7" {
		t.Fatal(err, fs)
	}
	fs, err = ParseTagFilters("col%61:r%65d")
	if err != nil || fs[0].Key != "cola" || fs[0].Value != "red" {
		t.Fatal(err, fs)
	}
	for _, bad := range []string{"bad", "a:", ":b", "a<x", "a:b,,c:d", "a b:c", "a%zz:b", "a%2:b"} {
		if _, err := ParseTagFilters(bad); err == nil {
			t.Errorf("%q must fail", bad)
		}
	}
	_, err = ParseTagFilters("n<abc")
	if e, ok := err.(*Error); !ok || e.Kind != KindBadRequest || e.Msg != "Value cannot be parsed as long." {
		t.Fatal(err)
	}
	_, err = ParseTagFilters("bad")
	if e := err.(*Error); e.Msg != `The given String cannot be parsed as a TagFilter: "bad".` {
		t.Fatal(e.Msg)
	}
	_, err = ParseTagFilters("a%zz:b")
	if e := err.(*Error); e.Msg != `URLDecoder: Illegal hex characters in escape (%) pattern - For input string: "zz"` {
		t.Fatal(e.Msg)
	}
}

func TestPageJSON(t *testing.T) {
	p := MetadataPage{Content: []Metadata{{SecretID: "x", Tags: []Tag{{"k", "v", TypeString}}}}, Number: 0, Size: 2147483647, TotalElements: 1, TotalPages: 1}
	want := `{"content":[{"secretId":"x","tags":[{"key":"k","value":"v","valueType":"STRING"}]}],"number":0,"size":2147483647,"totalElements":1,"totalPages":1}`
	if p.JSON() != want {
		t.Fatal(p.JSON())
	}
}
