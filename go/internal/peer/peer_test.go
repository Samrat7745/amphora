package peer

import (
	"context"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/carbynestack/amphora-go/internal/model"
)

func exch() model.ExchangeObject {
	return model.ExchangeObject{OperationID: "ea983b9b-0e98-4cbb-8dbf-3c8362653c0d", PlayerID: 1,
		Values: []model.FactorPair{{A: big.NewInt(4), B: big.NewInt(-5)}}}
}

func TestNewRejectsPartnerWithoutHost(t *testing.T) {
	for _, p := range []string{"nohost", "", "/path"} {
		if _, err := New([]string{p}, false, nil, time.Second); err == nil {
			t.Errorf("%q must be rejected", p)
		}
	}
}

func TestOpenPostsToEveryPartner(t *testing.T) {
	var mu sync.Mutex
	got := map[string]string{}
	mk := func(name string, status int) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			mu.Lock()
			got[name] = r.Method + " " + r.URL.Path + " " + r.Header.Get("Content-Type") + " " + string(b)
			mu.Unlock()
			w.WriteHeader(status)
		}))
	}
	a, b := mk("a", 202), mk("b", 202)
	defer a.Close()
	defer b.Close()
	c, err := New([]string{a.URL + "/", b.URL + "/amphora"}, false, nil, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Open(context.Background(), exch()); err != nil {
		t.Fatal(err)
	}
	want := "POST /inter-vcp/open application/json " + exch().JSON()
	if got["a"] != want || got["b"] != "POST /amphora/inter-vcp/open application/json "+exch().JSON() {
		t.Fatalf("%v", got)
	}
	if !strings.Contains(exch().JSON(), `"b":-5`) {
		t.Fatal(exch().JSON())
	}
}

func TestOpenFailureOfOnePartner(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(202) }))
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	defer ok.Close()
	defer bad.Close()
	c, _ := New([]string{ok.URL, bad.URL}, false, nil, 5*time.Second)
	err := c.Open(context.Background(), exch())
	want := `Request for endpoint "` + bad.URL + `/inter-vcp/open" has failed with status 500`
	if err == nil || err.Error() != want {
		t.Fatalf("%v", err)
	}
	// unreachable partner
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := dead.URL
	dead.Close()
	c, _ = New([]string{url}, false, nil, time.Second)
	if err := c.Open(context.Background(), exch()); err == nil {
		t.Fatal("unreachable partner must fail")
	}
}
