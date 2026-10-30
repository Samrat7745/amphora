package javaser

import (
	"bytes"
	"math/big"
	"testing"
)

// Fuzz targets for the Java serialization reader (data read back from Redis or received from Castor may be
// corrupt: the readers must return an error, never panic or allocate unbounded memory) and encoder round trips.

func seedStreams(f *testing.F) {
	f.Add(EncodeInputMaskList(nil))
	f.Add(EncodeInputMaskList([]Share{{Value: bytes.Repeat([]byte{1}, 16), Mac: bytes.Repeat([]byte{2}, 16)}}))
	f.Add(EncodeInputMaskListFromCastor([]Share{{Value: make([]byte, 16), Mac: make([]byte, 16)}, {Value: []byte{1}, Mac: nil}}))
	f.Add(EncodeExchangeObject(ExchangeObject{OperationID: "2affd2b3-bee6-4a25-9943-1fd19ff6f3b6", PlayerID: 1,
		Values: []FactorPair{{A: big.NewInt(-5), B: new(big.Int).Lsh(big.NewInt(1), 200)}}}))
	f.Add(EncodeExchangeObject(ExchangeObject{OperationID: "2affd2b3-bee6-4a25-9943-1fd19ff6f3b6"}))
	f.Add(EncodeTupleChunk("2affd2b3-bee6-4a25-9943-1fd19ff6f3b6", "INPUT_MASK_GFP", []byte{1, 2, 3}))
	f.Add([]byte{0xac, 0xed, 0x00, 0x05})
	f.Add([]byte{0xac, 0xed, 0x00, 0x05, 0x73})
	f.Add([]byte{})
	f.Add([]byte{0xac, 0xed, 0x00, 0x05, 0x75, 0x72, 0x00, 0x02, 0x5b, 0x42, 0, 0, 0, 0, 0, 0, 0, 0, 2, 0, 0, 0x7f, 0xff, 0xff, 0xff})
	// handle references to themselves / deep nesting
	f.Add(append([]byte{0xac, 0xed, 0x00, 0x05}, bytes.Repeat([]byte{0x70}, 20000)...))
	f.Add(append([]byte{0xac, 0xed, 0x00, 0x05}, bytes.Repeat([]byte{0x71, 0, 0x7e, 0, 0}, 5000)...))
}

func FuzzDecodeInputMaskList(f *testing.F) {
	seedStreams(f)
	f.Fuzz(func(t *testing.T, b []byte) {
		shares, err := DecodeInputMaskList(b)
		if err == nil && shares == nil {
			t.Fatal("nil slice without error")
		}
	})
}

func FuzzDecodeExchangeObject(f *testing.F) {
	seedStreams(f)
	f.Fuzz(func(t *testing.T, b []byte) {
		e, err := DecodeExchangeObject(b)
		if err != nil {
			return
		}
		// anything the decoder accepts re-encodes and decodes to the same value
		got, err := DecodeExchangeObject(EncodeExchangeObject(*e))
		if err != nil || got.OperationID != e.OperationID || got.PlayerID != e.PlayerID || len(got.Values) != len(e.Values) || (got.Values == nil) != (e.Values == nil) {
			t.Fatalf("re-encode round trip failed: %v %+v vs %+v", err, got, e)
		}
	})
}

func FuzzDecodeUploadResponse(f *testing.F) {
	seedStreams(f)
	f.Fuzz(func(t *testing.T, b []byte) { _, _ = DecodeUploadResponse(b) })
}

func FuzzRoundTripInputMaskList(f *testing.F) {
	f.Add([]byte("0123456789abcdef"), []byte("fedcba9876543210"), uint8(3))
	f.Add([]byte{}, []byte{}, uint8(0))
	f.Add(bytes.Repeat([]byte{0xff}, 300), []byte{0}, uint8(255))
	f.Fuzz(func(t *testing.T, v, m []byte, n uint8) {
		in := make([]Share, int(n)%8)
		for i := range in {
			in[i] = Share{Value: v, Mac: m}
		}
		for _, enc := range []func([]Share) []byte{EncodeInputMaskList, EncodeInputMaskListFromCastor} {
			got, err := DecodeInputMaskList(enc(in))
			if err != nil || len(got) != len(in) {
				t.Fatalf("%v %d", err, len(got))
			}
			for i := range got {
				if !bytes.Equal(got[i].Value, v) || !bytes.Equal(got[i].Mac, m) {
					t.Fatal("share differs")
				}
			}
		}
	})
}

func FuzzRoundTripExchangeObject(f *testing.F) {
	f.Add([]byte{1, 2, 3}, []byte{0xff, 0xff}, true, int32(-7), uint8(2), true)
	f.Add([]byte{}, []byte{}, false, int32(0), uint8(0), false)
	f.Fuzz(func(t *testing.T, a, b []byte, neg bool, player int32, n uint8, nilList bool) {
		x := new(big.Int).SetBytes(a)
		y := new(big.Int).SetBytes(b)
		if neg {
			x.Neg(x)
		}
		e := ExchangeObject{OperationID: "2affd2b3-bee6-4a25-9943-1fd19ff6f3b6", PlayerID: player}
		if !nilList {
			e.Values = []FactorPair{}
			for i := 0; i < int(n)%6; i++ {
				e.Values = append(e.Values, FactorPair{A: x, B: y})
			}
		}
		got, err := DecodeExchangeObject(EncodeExchangeObject(e))
		if err != nil {
			t.Fatal(err)
		}
		if got.PlayerID != player || got.OperationID != e.OperationID || (got.Values == nil) != nilList || len(got.Values) != len(e.Values) {
			t.Fatalf("%+v vs %+v", got, e)
		}
		for _, p := range got.Values {
			if p.A.Cmp(x) != 0 || p.B.Cmp(y) != 0 {
				t.Fatal("big integer differs")
			}
		}
	})
}
