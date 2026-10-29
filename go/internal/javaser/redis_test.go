package javaser

import (
	"bytes"
	"math/big"
	"os"
	"testing"
)

func fx(t *testing.T, n string) []byte {
	b, err := os.ReadFile("testdata/" + n)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func sharesFromRaw(raw []byte) []Share {
	var out []Share
	for i := 0; i+32 <= len(raw); i += 32 {
		out = append(out, Share{Value: raw[i : i+16], Mac: raw[i+16 : i+32]})
	}
	return out
}

func TestInputMaskListGolden(t *testing.T) {
	for _, n := range []string{"inputmask_list3", "inputmask_list0", "inputmask_list1_zero"} {
		want := fx(t, n+".bin")
		var shares []Share
		switch n {
		case "inputmask_list3":
			shares = sharesFromRaw(fx(t, "inputmask_list3.raw"))
		case "inputmask_list1_zero":
			shares = sharesFromRaw(make([]byte, 32))
		}
		if got := EncodeInputMaskList(shares); !bytes.Equal(got, want) {
			t.Fatalf("%s: encode mismatch\n got %x\nwant %x", n, got, want)
		}
		dec, err := DecodeInputMaskList(want)
		if err != nil || len(dec) != len(shares) {
			t.Fatalf("%s decode: %v %d", n, err, len(dec))
		}
		for i := range dec {
			if !bytes.Equal(dec[i].Value, shares[i].Value) || !bytes.Equal(dec[i].Mac, shares[i].Mac) {
				t.Fatalf("%s share %d differs", n, i)
			}
		}
	}
}

func bi(s string) *big.Int { v, _ := new(big.Int).SetString(s, 10); return v }

func TestExchangeObjectGolden(t *testing.T) {
	big1 := "198766463529478683931867765928436695040"
	e := ExchangeObject{OperationID: "123e4567-e89b-12d3-a456-426614174000", PlayerID: 2, Values: []FactorPair{
		{bi("0"), bi("1")}, {bi(big1), bi("-" + big1)}, {bi("-1"), bi("255")}, {bi("256"), bi("-256")},
		{bi("340282366920938463463374607431768211456"), bi("-170141183460469231731687303715884105728")}}}
	want := fx(t, "meo.bin")
	if got := EncodeExchangeObject(e); !bytes.Equal(got, want) {
		t.Fatalf("meo encode mismatch\n got %x\nwant %x", got, want)
	}
	d, err := DecodeExchangeObject(want)
	if err != nil || d.PlayerID != 2 || d.OperationID != e.OperationID || len(d.Values) != 5 {
		t.Fatal(err, d)
	}
	for i := range d.Values {
		if d.Values[i].A.Cmp(e.Values[i].A) != 0 || d.Values[i].B.Cmp(e.Values[i].B) != 0 {
			t.Fatalf("pair %d", i)
		}
	}
	empty := ExchangeObject{OperationID: e.OperationID, PlayerID: 0, Values: []FactorPair{}}
	if got := EncodeExchangeObject(empty); !bytes.Equal(got, fx(t, "meo_empty.bin")) {
		t.Fatal("empty meo mismatch")
	}
	// null list/operation id (fields absent in the request), as observed from the real Java service
	null := EncodeExchangeObject(ExchangeObject{})
	if !bytes.HasSuffix(null, []byte{0x70, 0x70}) {
		t.Fatalf("null meo should end in two TC_NULL: %x", null)
	}
	if d, err := DecodeExchangeObject(null); err != nil || d.Values != nil {
		t.Fatal(err, d)
	}
}

func TestRejectsOther(t *testing.T) {
	if _, err := DecodeInputMaskList(fx(t, "meo.bin")); err == nil {
		t.Fatal("must reject")
	}
	if _, err := DecodeExchangeObject([]byte("nonsense")); err == nil {
		t.Fatal("must reject")
	}
}
