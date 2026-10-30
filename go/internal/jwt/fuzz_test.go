package jwt

import (
	"encoding/base64"
	"errors"
	"testing"

	"github.com/carbynestack/amphora-go/internal/model"
)

// FuzzUserID: the claim extraction runs on unauthenticated input. It must never panic and every failure must be a
// client error (400/401), never a 500.
func FuzzUserID(f *testing.F) {
	enc := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	f.Add("Bearer a."+enc(`{"sub":"user-1"}`)+".c", "sub")
	f.Add("Bearer a."+enc(`{"a":{"b":{"c":12345678901234567890}}}`)+".c", "a.b.c")
	f.Add("Bearer a."+enc(`{"sub":null}`)+".c", "sub")
	f.Add("Bearer a."+enc(`{"sub":[1,2]}`)+".c", "sub")
	f.Add("Bearer a."+enc(`[`)+".c", "sub")
	f.Add("Bearer a.@@@.c", "sub")
	f.Add("Bearer a."+base64.URLEncoding.EncodeToString([]byte{0xfb, 0xff})+".c", "sub")
	f.Add("Bearer ..", "")
	f.Add("Bearer", "sub")
	f.Add("", "")
	f.Add("Bearer a.b.c.d", "a..b")
	f.Add("Bearer a."+enc(`{"":{"":1}}`)+".c", ".")
	f.Fuzz(func(t *testing.T, header, field string) {
		id, err := Reader{Field: field}.UserID(header)
		if err == nil {
			_ = id
			return
		}
		var me *model.Error
		if !errors.As(err, &me) || me.Kind == model.KindInternal {
			t.Fatalf("non client error: %T %v", err, err)
		}
	})
}
