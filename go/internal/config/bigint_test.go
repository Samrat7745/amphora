package config

// Port of BigIntegerConverterTest.

import (
	"fmt"
	"math/big"
	"strings"
	"testing"
)

func TestBigIntegerConverter(t *testing.T) {
	t.Run("givenValidBigIntegerAsString_whenConvert_thenReturnBigIntegerObject", func(t *testing.T) {
		got, err := ParseBigInteger("42")
		if err != nil || got.Cmp(big.NewInt(42)) != 0 {
			t.Fatal(got, err)
		}
		for in, want := range map[string]string{"-33717010807885571165607137982809795379": "-33717010807885571165607137982809795379", "+7": "7", "198766463529478683931867765928436695041": "198766463529478683931867765928436695041"} {
			if got, err := ParseBigInteger(in); err != nil || got.String() != want {
				t.Errorf("%s: %v %v", in, got, err)
			}
		}
	})
	t.Run("givenEmptyString_whenConvert_thenThrowException", func(t *testing.T) {
		_, err := ParseBigInteger("")
		if err == nil || err.Error() != fmt.Sprintf(InvalidBigIntegerMsg, "") || err.Error() != `Invalid value "" for type BigInteger` {
			t.Fatal(err)
		}
	})
	t.Run("malformed input is rejected", func(t *testing.T) {
		for _, in := range []string{"abc", "1.5", " 1", "0x10", "1_0"} {
			if _, err := ParseBigInteger(in); err == nil {
				t.Errorf("%q must fail", in)
			}
		}
	})
	t.Run("givenNull_whenConvert_thenThrowException (unset variable)", func(t *testing.T) {
		// A Spring null property value is an unset environment variable in the Go service.
		m := base()
		delete(m, "SPDZ_R")
		_, err := LoadFrom(env(m))
		if err == nil || !strings.Contains(err.Error(), `Invalid value "" for type BigInteger`) || !strings.Contains(err.Error(), "SPDZ_R") {
			t.Fatal(err)
		}
	})
}

// AmphoraServiceSystemTest.givenMultipleVCPartnersDefined_whenInitialized_thenHoldExpectedConfiguration
func TestMultipleVCPartnersDefined(t *testing.T) {
	m := base()
	m["AMPHORA_VC_PARTNERS"] = "http://amphora2.carbynestack.io,http://amphora3.carbynestack.io"
	c, err := LoadFrom(env(m))
	if err != nil || c.Validate() != nil {
		t.Fatal(err, c.Validate())
	}
	if want := []string{"http://amphora2.carbynestack.io", "http://amphora3.carbynestack.io"}; !equalStrings(c.VCPartners, want) {
		t.Fatalf("%v", c.VCPartners)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
