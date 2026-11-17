package jwt

import (
	"encoding/base64"
	"testing"

	"github.com/Samrat7745/amphora/go/internal/model"
)

func token(payload string) string {
	return "Bearer h." + base64.StdEncoding.EncodeToString([]byte(payload)) + ".s"
}

func status(err error) (model.Kind, string) {
	if e, ok := err.(*model.Error); ok {
		return e.Kind, e.Msg
	}
	return -1, err.Error()
}

func TestUserID(t *testing.T) {
	r := Reader{Field: "sub"}
	got, err := r.UserID(token(`{"sub":"alice"}`))
	if err != nil || got != "alice" {
		t.Fatal(got, err)
	}
	if got, _ := (Reader{Field: "a.b.c"}).UserID(token(`{"a":{"b":{"c":42}}}`)); got != "42" {
		t.Fatal(got)
	}
	if got, _ := r.UserID(token(`{"sub":null}`)); got != "null" {
		t.Fatal("NullNode.asText is \"null\":", got)
	}
	if got, _ := r.UserID(token(`{"sub":{"x":1}}`)); got != "" {
		t.Fatal("container asText is empty:", got)
	}
	if got, err := r.UserID(token(`{"sub":"x"} trailing`)); err != nil || got != "x" {
		t.Fatal("Jackson ignores trailing content", got, err)
	}
	for _, bad := range []string{"", "Basic x", "bearer x.y.z", "Bearer a.b", "Bearer a.b.c.d", token(`nope`), token(`{"x":1}`), token(`[1]`), token(``)} {
		_, err := r.UserID(bad)
		if k, m := status(err); k != model.KindUnauthorized || m != "No token provided" {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

func TestURLSafeBase64Quirk(t *testing.T) {
	r := Reader{Field: "sub"}
	_, err := r.UserID("Bearer a.ab_c.d")
	if k, m := status(err); k != model.KindBadRequest || m != "Illegal base64 character 5f" {
		t.Fatal(k, m)
	}
	_, err = r.UserID("Bearer a.ab-c.d")
	if _, m := status(err); m != "Illegal base64 character 2d" {
		t.Fatal(m)
	}
	// unpadded standard base64 is accepted
	if got, err := r.UserID("Bearer a." + base64.RawStdEncoding.EncodeToString([]byte(`{"sub":"abc"}`)) + ".s"); err != nil || got != "abc" {
		t.Fatal(got, err)
	}
}
