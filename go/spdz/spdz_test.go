package spdz

import (
	"bufio"
	"encoding/hex"
	"math/big"
	"os"
	"strings"
	"testing"
)

// vectors.txt is generated from the real mp-spdz-integration 0.2.2 jar (tools/fixturegen/GenSpdz.java).
func TestGoldenVectors(t *testing.T) {
	f, err := os.Open("testdata/vectors.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var u *Utils
	n := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		parts := strings.SplitN(sc.Text(), " ", 3)
		switch parts[0] {
		case "params":
			ps := strings.Split(sc.Text(), " ")
			p, _ := new(big.Int).SetString(ps[1], 10)
			r, _ := new(big.Int).SetString(ps[2], 10)
			ri, _ := new(big.Int).SetString(ps[3], 10)
			u = New(p, r, ri)
		case "to":
			v, _ := new(big.Int).SetString(parts[1], 10)
			b, err := u.ToGfp(v)
			if strings.HasPrefix(parts[2], "ERR ") {
				if err == nil || "IllegalArgumentException: "+err.Error() != strings.TrimPrefix(parts[2], "ERR ") {
					t.Fatalf("to %s: want %s got %v", parts[1], parts[2], err)
				}
			} else if err != nil || hex.EncodeToString(b) != parts[2] {
				t.Fatalf("to %s: want %s got %x %v", parts[1], parts[2], b, err)
			}
		case "from":
			b, _ := hex.DecodeString(parts[1])
			v, err := u.FromGfp(b)
			if strings.HasPrefix(parts[2], "ERR ") {
				if err == nil || "IllegalArgumentException: "+err.Error() != strings.TrimPrefix(parts[2], "ERR ") {
					t.Fatalf("from %s: want %s got %v", parts[1], parts[2], err)
				}
			} else if err != nil || v.String() != parts[2] {
				t.Fatalf("from %s: want %s got %v %v", parts[1], parts[2], v, err)
			}
		case "uuid":
			b, _ := hex.DecodeString(parts[1])
			if got := NameUUIDFromBytes(b); got != parts[2] {
				t.Fatalf("uuid %s: want %s got %s", parts[1], parts[2], got)
			}
		}
		n++
	}
	if n < 600 {
		t.Fatalf("only %d vectors", n)
	}
}
