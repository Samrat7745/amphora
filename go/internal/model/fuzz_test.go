package model

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

// Fuzz targets for the HTTP-facing parsers. Invariants: never panic; every failure is a client error (a
// *Error whose Kind maps to 4xx), never an internal (5xx) one. Seed corpora live in testdata/fuzz/<Target>.

func requireClientError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	var me *Error
	if !errors.As(err, &me) {
		t.Fatalf("parser returned a non model error (would be a 500): %T %v", err, err)
	}
	if me.Kind == KindInternal {
		t.Fatalf("parser returned an internal (5xx) error: %v", err)
	}
}

var jsonSeeds = []string{
	`{"secretId":"3bcf8308-8f50-4d24-a37b-b0075bb5e779","data":[{"value":"AAAAAAAAAAAAAAAAAAAAAA=="}],"tags":[{"key":"k","value":"v","valueType":"STRING"}]}`,
	`{"secretId":"3bcf8308-8f50-4d24-a37b-b0075bb5e779","data":"QUJD","tags":null}`,
	`{"secretId":"3bcf8308-8f50-4d24-a37b-b0075bb5e779","secretId":"x"}`,
	`{"operationId":"3bcf8308-8f50-4d24-a37b-b0075bb5e779","playerId":1,"interimValues":[{"a":123456789012345678901234567890123456789012345678901234567890,"b":-1e5}]}`,
	`{"interimValues":[{"a":1e999999999,"b":0.5}]}`,
	`{"data":[{"value":"@@@@"}],"secretId":"x"}`,
	`{"data":[{"value":"QQ"},null,{}]}`,
	`null`, `[]`, `{}`, ``, `{`, `"`, `1`,
	`{"tags":[{"key":"k"},null,{"key":null},{"key":"k","valueType":"LONG","value":"9999999999999999999999"}]}`,
	strings.Repeat(`[`, 5000) + strings.Repeat(`]`, 5000),
	`{"a":` + strings.Repeat(`{"a":`, 2000) + `1` + strings.Repeat(`}`, 2001),
}

func addJSONSeeds(f *testing.F) {
	for _, s := range jsonSeeds {
		f.Add([]byte(s))
	}
}

func FuzzParseMaskedInput(f *testing.F) {
	addJSONSeeds(f)
	f.Fuzz(func(t *testing.T, b []byte) {
		mi, err := ParseMaskedInput(b)
		requireClientError(t, err)
		if err == nil && mi == nil {
			t.Fatal("nil result without error")
		}
	})
}

func FuzzParseSecretShare(f *testing.F) {
	addJSONSeeds(f)
	f.Fuzz(func(t *testing.T, b []byte) {
		s, err := ParseSecretShare(b)
		requireClientError(t, err)
		if err == nil {
			if s == nil || len(s.Data)%SecretShareWidth != 0 {
				t.Fatalf("invalid share accepted: %+v", s)
			}
			_ = s.JSON() // rendering an accepted share must not panic either
		}
	})
}

func FuzzParseExchangeObject(f *testing.F) {
	addJSONSeeds(f)
	f.Fuzz(func(t *testing.T, b []byte) {
		e, err := ParseExchangeObject(b)
		requireClientError(t, err)
		if err == nil {
			_ = e.JSON()
		}
	})
}

func FuzzParseTag(f *testing.F) {
	addJSONSeeds(f)
	f.Add([]byte(`{"key":"a.b-c_d","value":"1","valueType":"LONG"}`))
	f.Fuzz(func(t *testing.T, b []byte) {
		tg, err := ParseTag(b)
		requireClientError(t, err)
		if err == nil {
			if ValidateKey(tg.Key) != nil || tg.ValueType.ValidateValue(tg.Value) != nil {
				t.Fatalf("invalid tag accepted: %v", tg)
			}
		}
		tags, err := ParseTagList(b)
		requireClientError(t, err)
		_ = TagsJSON(tags)
	})
}

func FuzzParseTagFilters(f *testing.F) {
	for _, s := range []string{"a:b", "n>5,m<7", "k:v,", ",", "", "a:b:c", "%41:%zz", "k:%", "a%3Ab:c", "x<abc", "k>99999999999999999999",
		strings.Repeat("a", 200) + ":1", "ü:ö", "a:b,,c:d", "a=b", "+:+", "k:%C3%28"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		fs, err := ParseTagFilters(s)
		requireClientError(t, err)
		for _, fl := range fs {
			if fl.Op != OpEquals && fl.Op != OpLessThan && fl.Op != OpGreaterThan {
				t.Fatalf("bad operator %q", fl.Op)
			}
			if ValidateKey(fl.Key) != nil {
				t.Fatalf("invalid key accepted: %q", fl.Key)
			}
		}
		_, err = ParseTagFilter(s)
		requireClientError(t, err)
	})
}

func FuzzURLDecode(f *testing.F) {
	for _, s := range []string{"", "abc", "a+b", "%41", "%4", "%", "%%41", "%zz", "%+1", "%-1", "%C3%A4", "%FF%FE", "100%", "%00", "a%2",
		strings.Repeat("%41", 1000)} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		out, err := URLDecode(s)
		if err != nil {
			if !strings.HasPrefix(err.Error(), "URLDecoder:") {
				t.Fatalf("unexpected error text %q", err)
			}
			return
		}
		// input without escapes is returned verbatim (also when it is not UTF-8); decoding escapes always yields UTF-8
		if strings.ContainsAny(s, "%+") && !utf8.ValidString(out) {
			t.Fatalf("output is not valid UTF-8: %q", out)
		}
		if !strings.ContainsAny(s, "%+") && out != s {
			t.Fatalf("string without escapes must be unchanged: %q -> %q", s, out)
		}
	})
}
