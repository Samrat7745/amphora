//go:build integration

package client

import (
	"context"
	"encoding/base64"
	"math/big"
	"os"
	"strings"
	"testing"

	"github.com/Samrat7745/amphora/go/internal/testkit"
	"github.com/google/uuid"
)

// Live run against two parties:
//
//	AMPHORA_URLS=http://127.0.0.1:11000,http://127.0.0.1:11001 CASTOR_URLS=http://127.0.0.1:12100,http://127.0.0.1:12101 \
//	  go test -tags integration -run TestLive ./client
//
// Needs a Castor pair without leftover tuples from other suites: the parity harness uploads tuples that are not
// consistent across the two parties, and masks drawn from those make the (correct) integrity check fail. Use a fresh rig.
// JAVA_ORACLE=1 tolerates the Java service's known always-failing tag overwrite (PUT /tags).
func TestLive(t *testing.T) {
	amphoras, castors := os.Getenv("AMPHORA_URLS"), os.Getenv("CASTOR_URLS")
	if amphoras == "" || castors == "" {
		t.Skip("AMPHORA_URLS / CASTOR_URLS not set")
	}
	sp := testkit.Params()
	keys := []*big.Int{big.NewInt(1234567), big.NewInt(7654321)}
	for _, tt := range []string{"INPUT_MASK_GFP", "MULTIPLICATION_TRIPLE_GFP"} {
		if err := testkit.UploadTuples(sp, strings.Split(castors, ","), keys, tt, 500); err != nil {
			t.Fatalf("upload %s: %v", tt, err)
		}
	}
	tok := "e30." + base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"alice"}`)) + ".sig"
	c, err := New(Options{ServiceAddresses: strings.Split(amphoras, ","), Prime: sp.Prime, R: sp.R, RInv: sp.RInv,
		BearerTokenProvider: func(string) string { return tok }})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	data := []*big.Int{big.NewInt(42), big.NewInt(0), new(big.Int).Sub(sp.Prime, big.NewInt(1))}
	s := NewSecret([]Tag{{Key: "purpose", Value: "it"}, {Key: "rank", Value: "7", ValueType: TagLong}}, data)
	id, err := c.CreateSecret(ctx, s)
	if err != nil || id != s.SecretID {
		t.Fatalf("create: %v", err)
	}
	got, err := c.GetSecret(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	for i := range data {
		if got.Data[i].Cmp(data[i]) != 0 {
			t.Fatalf("element %d: got %v want %v", i, got.Data[i], data[i])
		}
	}
	has := func(tags []Tag, k, v string) bool {
		for _, tg := range tags {
			if tg.Key == k && tg.Value == v {
				return true
			}
		}
		return false
	}
	if !has(got.Tags, "purpose", "it") || !has(got.Tags, "owner", "alice") {
		t.Fatalf("tags %+v", got.Tags)
	}
	// tags
	if err := c.CreateTag(ctx, id, Tag{Key: "extra", Value: "x"}); err != nil {
		t.Fatal(err)
	}
	tg, err := c.GetTag(ctx, id, "extra")
	if err != nil || tg.Value != "x" {
		t.Fatal(err, tg)
	}
	if err := c.UpdateTag(ctx, id, Tag{Key: "extra", Value: "y"}); err != nil {
		t.Fatal(err)
	}
	if tg, _ = c.GetTag(ctx, id, "extra"); tg == nil || tg.Value != "y" {
		t.Fatalf("update not visible: %+v", tg)
	}
	if err := c.DeleteTag(ctx, id, "extra"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetTag(ctx, id, "extra"); err == nil {
		t.Fatal("deleted tag still readable")
	}
	if err := c.OverwriteTags(ctx, id, []Tag{{Key: "only", Value: "1"}}); err != nil {
		if os.Getenv("JAVA_ORACLE") != "1" {
			t.Fatalf("overwrite: %v", err)
		}
		t.Logf("overwrite failed as in the Java service: %v", err)
	}
	// list / filter / page
	ms, err := c.GetSecrets(ctx, []TagFilter{{Key: "owner", Operator: OpEquals, Value: "alice"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range ms {
		if m.SecretID == id.String() {
			found = true
		}
	}
	if !found {
		t.Fatalf("secret %s not in filtered list (%d entries)", id, len(ms))
	}
	pg, err := c.GetSecretsPage(ctx, nil, PageRequest{Page: 0, Size: 1})
	if err != nil || len(pg.Content) > 1 || pg.TotalElements < 1 {
		t.Fatalf("page: %v %+v", err, pg)
	}
	// delete
	if err := c.DeleteSecret(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetSecret(ctx, id); err == nil {
		t.Fatal("deleted secret still readable")
	}
	if _, err := c.GetSecret(ctx, uuid.New()); err == nil {
		t.Fatal("unknown secret must fail")
	}
}
