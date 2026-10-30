package javaser

import (
	"bytes"
	"crypto/rand"
	"math/big"
	"testing"
)

// Randomized round trips (AmphoraTestData.getRandomInputMaskList / MultiplicationExchangeObject
// fixtures of InputMaskStoreRedisIT, InputMaskStoreTest, InterimValueCachingServiceTest): whatever the
// service stores in Redis must read back unchanged. Byte-exact compatibility with the Java stream is
// covered by the golden tests in redis_test.go.

func TestInputMaskListRandomRoundTrip(t *testing.T) {
	for _, n := range []int{0, 1, 5, 42, 300} {
		shares := make([]Share, n)
		for i := range shares {
			shares[i] = Share{Value: make([]byte, 16), Mac: make([]byte, 16)}
			_, _ = rand.Read(shares[i].Value)
			_, _ = rand.Read(shares[i].Mac)
		}
		for _, enc := range []func([]Share) []byte{EncodeInputMaskList, EncodeInputMaskListFromCastor} {
			got, err := DecodeInputMaskList(enc(shares))
			if err != nil || len(got) != n {
				t.Fatalf("n=%d: %v %d", n, err, len(got))
			}
			for i := range got {
				if !bytes.Equal(got[i].Value, shares[i].Value) || !bytes.Equal(got[i].Mac, shares[i].Mac) {
					t.Fatalf("n=%d share %d differs", n, i)
				}
			}
		}
	}
}

func TestExchangeObjectRandomRoundTrip(t *testing.T) {
	for _, n := range []int{0, 1, 7, 49} {
		e := ExchangeObject{OperationID: "2affd2b3-bee6-4a25-9943-1fd19ff6f3b6", PlayerID: int32(n * 2)}
		e.Values = []FactorPair{}
		for i := 0; i < n; i++ {
			a, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 130))
			b, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 130))
			if i%2 == 1 {
				a.Neg(a)
			}
			e.Values = append(e.Values, FactorPair{A: a, B: b})
		}
		got, err := DecodeExchangeObject(EncodeExchangeObject(e))
		if err != nil || got.OperationID != e.OperationID || got.PlayerID != e.PlayerID || len(got.Values) != n {
			t.Fatalf("n=%d: %v %+v", n, err, got)
		}
		for i := range got.Values {
			if got.Values[i].A.Cmp(e.Values[i].A) != 0 || got.Values[i].B.Cmp(e.Values[i].B) != 0 {
				t.Fatalf("n=%d pair %d differs", n, i)
			}
		}
	}
}
