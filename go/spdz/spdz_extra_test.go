package spdz

import (
	"crypto/rand"
	"math/big"
	"testing"
)

// Complements the golden vectors (generated from the real mp-spdz-integration jar) with round trips
// over random field elements and the java.util.UUID.nameUUIDFromBytes definition used for the
// derived operation ids (the amphora-java-client/service tests use these utilities everywhere).

func testUtils() *Utils {
	p, _ := new(big.Int).SetString("198766463529478683931867765928436695041", 10)
	r, _ := new(big.Int).SetString("141515903391459779531506841503331516415", 10)
	ri, _ := new(big.Int).SetString("133854242216446749056083838363708373830", 10)
	return New(p, r, ri)
}

func TestRandomRoundTrip(t *testing.T) {
	u := testUtils()
	for i := 0; i < 500; i++ {
		v, _ := rand.Int(rand.Reader, u.Prime)
		b, err := u.ToGfp(v)
		if err != nil || len(b) != WordWidth {
			t.Fatal(err, len(b))
		}
		back, err := u.FromGfp(b)
		if err != nil || back.Cmp(v) != 0 {
			t.Fatalf("%v != %v (%v)", back, v, err)
		}
	}
	// boundaries
	for _, v := range []*big.Int{big.NewInt(0), big.NewInt(1), new(big.Int).Sub(u.Prime, big.NewInt(1))} {
		b, err := u.ToGfp(v)
		if err != nil {
			t.Fatal(err)
		}
		if back, _ := u.FromGfp(b); back.Cmp(v) != 0 {
			t.Fatalf("%v", v)
		}
	}
}

func TestValidationMessages(t *testing.T) {
	u := testUtils()
	if _, err := u.FromGfp(make([]byte, 15)); err == nil || err.Error() != "Gfp byte representation must have a length of 16. Actual: 15." {
		t.Fatal(err)
	}
	if _, err := u.ToGfp(big.NewInt(-1)); err == nil || err.Error() != "Value must not be negative. Actual: -1." {
		t.Fatal(err)
	}
	tooBig := new(big.Int).Add(u.Prime, big.NewInt(1))
	if _, err := u.ToGfp(tooBig); err == nil || err.Error() != "Value must not be larger than "+u.Prime.String()+". Actual: "+tooBig.String()+"." {
		t.Fatal(err)
	}
}

func TestNameUUIDFromBytes(t *testing.T) {
	// java.util.UUID.nameUUIDFromBytes: MD5 with version 3 / IETF variant bits
	for in, want := range map[string]string{
		"":      "d41d8cd9-8f00-3204-a980-0998ecf8427e",
		"hello": "5d41402a-bc4b-3a76-b971-9d911017c592",
	} {
		if got := NameUUIDFromBytes([]byte(in)); got != want {
			t.Fatalf("%q: got %s want %s", in, got, want)
		}
	}
}
