package spdz

import (
	"bytes"
	"math/big"
	"math/rand"
	"testing"
	"testing/quick"
)

// Property tests with fixed seeds: gfp round trips, out-of-range behaviour, limb helpers and additive
// sharing with information theoretic MACs.

func randBelow(r *rand.Rand, n *big.Int) *big.Int {
	b := make([]byte, n.BitLen()/8+2)
	r.Read(b)
	return new(big.Int).Mod(new(big.Int).SetBytes(b), n)
}

func TestToFromGfpQuick(t *testing.T) {
	u := testUtils()
	f := func(hi, lo uint64) bool {
		v := new(big.Int).Lsh(new(big.Int).SetUint64(hi), 64)
		v.Or(v, new(big.Int).SetUint64(lo))
		v.Mod(v, u.Prime)
		b, err := u.ToGfp(v)
		if err != nil || len(b) != WordWidth {
			return false
		}
		back, err := u.FromGfp(b)
		return err == nil && back.Cmp(v) == 0
	}
	if err := quick.Check(f, &quick.Config{MaxCount: 2000, Rand: rand.New(rand.NewSource(1))}); err != nil {
		t.Fatal(err)
	}
}

// FromGfp never fails for 16 bytes and always yields a canonical residue; ToGfp(FromGfp(b)) is canonical for
// every input that is a valid Montgomery encoding (b < p), and the decode is stable under re-encoding.
func TestFromGfpAnyBytesIsReduced(t *testing.T) {
	u := testUtils()
	r := rand.New(rand.NewSource(2))
	for i := 0; i < 2000; i++ {
		b := make([]byte, WordWidth)
		r.Read(b)
		v, err := u.FromGfp(b)
		if err != nil {
			t.Fatal(err)
		}
		if v.Sign() < 0 || v.Cmp(u.Prime) >= 0 {
			t.Fatalf("not reduced: %v", v)
		}
		b2, err := u.ToGfp(v)
		if err != nil {
			t.Fatal(err)
		}
		v2, _ := u.FromGfp(b2)
		if v2.Cmp(v) != 0 {
			t.Fatal("re-encoding changed the value")
		}
	}
}

func TestEdgeValues(t *testing.T) {
	u := testUtils()
	one := big.NewInt(1)
	p := u.Prime
	max128 := new(big.Int).Sub(new(big.Int).Lsh(one, 128), one)

	ok := map[string]*big.Int{"0": big.NewInt(0), "1": one, "p-1": new(big.Int).Sub(p, one)}
	for name, v := range ok {
		b, err := u.ToGfp(v)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if back, _ := u.FromGfp(b); back.Cmp(v) != 0 {
			t.Fatalf("%s round trip: %v", name, back)
		}
	}
	// 0 encodes as all zero bytes
	if b, _ := u.ToGfp(big.NewInt(0)); !bytes.Equal(b, make([]byte, WordWidth)) {
		t.Fatalf("%x", b)
	}
	// Documented behaviour: the upper bound check is inclusive (Java: value > prime is rejected), so v == p is
	// accepted and encodes like 0 (p mod p); it decodes to 0.
	b, err := u.ToGfp(p)
	if err != nil {
		t.Fatalf("p must be accepted: %v", err)
	}
	if back, _ := u.FromGfp(b); back.Sign() != 0 {
		t.Fatalf("p decodes to %v", back)
	}
	// p+1, 2^128-1 and larger values are rejected with the Java message.
	for _, v := range []*big.Int{new(big.Int).Add(p, one), max128, new(big.Int).Lsh(one, 200)} {
		_, err := u.ToGfp(v)
		if err == nil || err.Error() != "Value must not be larger than "+p.String()+". Actual: "+v.String()+"." {
			t.Fatalf("%v: %v", v, err)
		}
	}
	// negative values are rejected
	if _, err := u.ToGfp(big.NewInt(-1)); err == nil || err.Error() != "Value must not be negative. Actual: -1." {
		t.Fatal(err)
	}
	// wrong lengths
	for _, n := range []int{0, 1, 15, 17, 32} {
		if _, err := u.FromGfp(make([]byte, n)); err == nil {
			t.Fatalf("length %d accepted", n)
		}
	}
	// 2^128-1 as raw bytes decodes to a reduced residue (no error)
	if v, err := u.FromGfp(bytes.Repeat([]byte{0xff}, 16)); err != nil || v.Cmp(p) >= 0 {
		t.Fatal(v, err)
	}
}

// The limb helpers (and their tests) live in the mp-spdz-integration library now; what stays here
// is the observable wire property: 1 in Montgomery form is R mod p, and the wire format is the
// byte-reversed big-endian value.
func TestOneEncodesAsR(t *testing.T) {
	u := testUtils()
	b, err := u.ToGfp(big.NewInt(1))
	if err != nil {
		t.Fatal(err)
	}
	be := make([]byte, len(b))
	for i := range b {
		be[len(b)-1-i] = b[i]
	}
	if new(big.Int).SetBytes(be).Cmp(u.R) != 0 {
		t.Fatalf("1 must encode as R, got %x", be)
	}
}

// Additive secret sharing with MACs: share_i = (x_i, m_i), sum x_i = x, sum m_i = alpha * x with alpha = sum alpha_i.
func TestAdditiveSharingWithMACs(t *testing.T) {
	u := testUtils()
	r := rand.New(rand.NewSource(4))
	mod := func(x *big.Int) *big.Int { return x.Mod(x, u.Prime) }
	for iter := 0; iter < 300; iter++ {
		n := 2 + r.Intn(4)
		x := randBelow(r, u.Prime)
		alphas := make([]*big.Int, n)
		alpha := new(big.Int)
		for i := range alphas {
			alphas[i] = randBelow(r, u.Prime)
			alpha.Add(alpha, alphas[i])
		}
		mod(alpha)
		split := func(secret *big.Int) []*big.Int {
			out := make([]*big.Int, n)
			sum := new(big.Int)
			for i := 0; i < n-1; i++ {
				out[i] = randBelow(r, u.Prime)
				sum.Add(sum, out[i])
			}
			out[n-1] = mod(new(big.Int).Sub(secret, sum))
			return out
		}
		xs := split(x)
		ms := split(mod(new(big.Int).Mul(alpha, x)))
		// serialise every share as value||mac (ShareWidth bytes), then reconstruct from the wire form
		sumX, sumM := new(big.Int), new(big.Int)
		for i := 0; i < n; i++ {
			vb, err := u.ToGfp(xs[i])
			if err != nil {
				t.Fatal(err)
			}
			mb, err := u.ToGfp(ms[i])
			if err != nil {
				t.Fatal(err)
			}
			wire := append(append([]byte{}, vb...), mb...)
			if len(wire) != ShareWidth {
				t.Fatal(len(wire))
			}
			v, _ := u.FromGfp(wire[:WordWidth])
			m, _ := u.FromGfp(wire[WordWidth:])
			sumX.Add(sumX, v)
			sumM.Add(sumM, m)
		}
		mod(sumX)
		mod(sumM)
		if sumX.Cmp(x) != 0 {
			t.Fatalf("shares do not sum to the secret: %v vs %v", sumX, x)
		}
		if sumM.Cmp(mod(new(big.Int).Mul(alpha, sumX))) != 0 {
			t.Fatal("MAC does not verify")
		}
		// tampering with any share value makes the MAC check fail (alpha != 0 with overwhelming probability)
		if alpha.Sign() != 0 {
			bad := new(big.Int).Add(sumX, big.NewInt(1))
			if sumM.Cmp(mod(new(big.Int).Mul(alpha, bad))) == 0 {
				t.Fatal("tampered value verified")
			}
		}
	}
}

func TestNameUUIDFromBytesVersionBits(t *testing.T) {
	r := rand.New(rand.NewSource(5))
	for i := 0; i < 100; i++ {
		b := make([]byte, r.Intn(64))
		r.Read(b)
		s := NameUUIDFromBytes(b)
		if len(s) != 36 || s[14] != '3' || !(s[19] == '8' || s[19] == '9' || s[19] == 'a' || s[19] == 'b') {
			t.Fatalf("%s", s)
		}
		if s != NameUUIDFromBytes(b) {
			t.Fatal("not deterministic")
		}
	}
}
