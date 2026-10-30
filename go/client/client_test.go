package client

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/carbynestack/amphora-go/spdz"
	"github.com/google/uuid"
)

func params() *spdz.Utils {
	p, _ := new(big.Int).SetString("198766463529478683931867765928436695041", 10)
	r, _ := new(big.Int).SetString("141515903391459779531506841503331516415", 10)
	ri, _ := new(big.Int).SetString("133854242216446749056083838363708373830", 10)
	return spdz.New(p, r, ri)
}

func rnd(p *big.Int) *big.Int { v, _ := rand.Int(rand.Reader, p); return v }

func split(p, v *big.Int, n int) []*big.Int {
	out := make([]*big.Int, n)
	sum := new(big.Int)
	for i := 0; i < n-1; i++ {
		out[i] = rnd(p)
		sum.Add(sum, out[i])
	}
	out[n-1] = new(big.Int).Mod(new(big.Int).Sub(v, sum), p)
	return out
}

// fakeODOs builds consistent output delivery objects for the given plaintext words (n parties).
func fakeODOs(t *testing.T, sp *spdz.Utils, words []*big.Int, n int, corrupt bool) []odo {
	t.Helper()
	odos := make([]odo, n)
	for _, s := range words {
		r, v := rnd(sp.Prime), rnd(sp.Prime)
		w := new(big.Int).Mod(new(big.Int).Mul(s, r), sp.Prime)
		u := new(big.Int).Mod(new(big.Int).Mul(v, r), sp.Prime)
		if corrupt {
			w.Add(w, big.NewInt(1))
		}
		parts := [][]*big.Int{split(sp.Prime, s, n), split(sp.Prime, r, n), split(sp.Prime, v, n), split(sp.Prime, w, n), split(sp.Prime, u, n)}
		for j := 0; j < n; j++ {
			enc := func(x *big.Int) []byte {
				b, err := sp.ToGfp(x)
				if err != nil {
					t.Fatal(err)
				}
				return b
			}
			odos[j].SecretShares = append(odos[j].SecretShares, enc(parts[0][j])...)
			odos[j].RShares = append(odos[j].RShares, enc(parts[1][j])...)
			odos[j].VShares = append(odos[j].VShares, enc(parts[2][j])...)
			odos[j].WShares = append(odos[j].WShares, enc(parts[3][j])...)
			odos[j].UShares = append(odos[j].UShares, enc(parts[4][j])...)
		}
	}
	return odos
}

func TestVerifyRecombinesAndDetectsTampering(t *testing.T) {
	sp := params()
	words := []*big.Int{big.NewInt(42), rnd(sp.Prime), big.NewInt(0)}
	got, err := verify(sp, fakeODOs(t, sp, words, 3, false))
	if err != nil {
		t.Fatal(err)
	}
	for i := range words {
		if got[i].Cmp(words[i]) != 0 {
			t.Fatalf("word %d: %v != %v", i, got[i], words[i])
		}
	}
	if _, err := verify(sp, fakeODOs(t, sp, words, 2, true)); err == nil {
		t.Fatal("tampered ODO must fail verification")
	} else if _, ok := err.(*IntegrityError); !ok {
		t.Fatalf("want IntegrityError, got %T", err)
	}
}

func TestFilterAndSortEncoding(t *testing.T) {
	if got := filterParam([]TagFilter{{"owner", OpEquals, "a b"}, {"rank", OpGreaterThan, "10"}}); got != "owner:a+b,rank>10" {
		t.Fatalf("filter %q", got)
	}
	var rq []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rq = append(rq, r.URL.RequestURI())
		_, _ = w.Write([]byte(`{"content":[{"secretId":"` + uuid.NewString() + `","tags":[{"key":"k","value":"v","valueType":"STRING"}]}],"number":1,"size":5,"totalElements":6,"totalPages":2}`))
	}))
	defer srv.Close()
	c, _ := New(Options{ServiceAddresses: []string{srv.URL}})
	ms, err := c.GetSecrets(context.Background(), []TagFilter{{"rank", OpLessThan, "5"}}, &Sort{"rank", true})
	if err != nil || len(ms) != 1 || ms[0].Tags[0].Key != "k" {
		t.Fatal(err, ms)
	}
	p, err := c.GetSecretsPage(context.Background(), nil, PageRequest{Page: 1, Size: 5, Sort: &Sort{Property: "rank"}})
	if err != nil || p.TotalPages != 2 || p.Number != 1 {
		t.Fatal(err, p)
	}
	if !strings.Contains(rq[0], "filter=rank%3C5") || !strings.Contains(rq[0], "sort=rank") || !strings.Contains(rq[0], "dir=DESC") {
		t.Fatalf("list request %q", rq[0])
	}
	if !strings.Contains(rq[1], "pageNumber=1") || !strings.Contains(rq[1], "pageSize=5") || !strings.Contains(rq[1], "dir=ASC") {
		t.Fatalf("page request %q", rq[1])
	}
}

func TestTagCallsAndErrorAggregation(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	mk := func(fail bool) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			calls = append(calls, r.Method+" "+r.URL.Path)
			mu.Unlock()
			if fail {
				http.Error(w, "nope", 403)
				return
			}
			_, _ = w.Write([]byte(`[{"key":"a","value":"1","valueType":"LONG"}]`))
		}))
	}
	ok, bad := mk(false), mk(true)
	defer ok.Close()
	defer bad.Close()
	id := uuid.New()
	c, _ := New(Options{ServiceAddresses: []string{ok.URL, ok.URL}})
	if err := c.CreateTag(context.Background(), id, Tag{"a", "1", TagLong}); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteTag(context.Background(), id, "a b"); err != nil {
		t.Fatal(err)
	}
	tags, err := c.GetTags(context.Background(), id)
	if err != nil || tags[0].ValueType != TagLong {
		t.Fatal(err, tags)
	}
	c2, _ := New(Options{ServiceAddresses: []string{ok.URL, bad.URL}})
	err = c2.OverwriteTags(context.Background(), id, nil)
	ce, isCE := err.(*ClientError)
	if !isCE || !strings.Contains(ce.Msg, "Request for endpoint "+bad.URL+" failed") || !strings.Contains(ce.Msg, "403") {
		t.Fatalf("aggregated error %v", err)
	}
	found := false
	for _, c := range calls {
		if strings.HasSuffix(c, "/tags/a%20b") || strings.HasSuffix(c, "/tags/a b") {
			found = true
		}
	}
	if !found {
		t.Fatalf("tag key not escaped in path: %v", calls)
	}
}

func TestIntraClientNoAuthAndRoundTrip(t *testing.T) {
	id := uuid.New()
	var auth string
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		if r.Method == "POST" {
			buf := new(strings.Builder)
			b := make([]byte, 1024)
			n, _ := r.Body.Read(b)
			buf.Write(b[:n])
			body = []byte(buf.String())
			w.WriteHeader(201)
			return
		}
		_, _ = w.Write([]byte(`{"secretId":"` + id.String() + `","tags":[],"data":"AAEC"}`))
	}))
	defer srv.Close()
	c, _ := NewIntraVcpClient(Options{ServiceAddresses: []string{srv.URL}, BearerTokenProvider: func(string) string { return "x" }})
	if _, err := c.UploadSecretShare(context.Background(), SecretShare{SecretID: id, Data: []byte{0, 1, 2}}); err != nil {
		t.Fatal(err)
	}
	var sent map[string]any
	if err := json.Unmarshal(body, &sent); err != nil || sent["data"] != "AAEC" {
		t.Fatal(err, string(body))
	}
	s, err := c.GetSecretShare(context.Background(), id)
	if err != nil || len(s.Data) != 3 || auth != "" {
		t.Fatal(err, s, "auth=", auth)
	}
}

func TestOptionsValidation(t *testing.T) {
	if _, err := New(Options{}); err == nil {
		t.Fatal("empty addresses must fail")
	}
	c, _ := New(Options{ServiceAddresses: []string{"http://127.0.0.1:1"}})
	if _, err := c.CreateSecret(context.Background(), NewSecret(nil, []*big.Int{big.NewInt(1)})); err == nil || !strings.Contains(err.Error(), "Prime") {
		t.Fatalf("missing field params must be reported: %v", err)
	}
}
