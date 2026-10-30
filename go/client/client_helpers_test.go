package client

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// Shared fixtures of the ports of the Java client tests (the Java tests mock the
// AmphoraCommunicationClient; here the real HTTP client talks to httptest "parties").

type recReq struct {
	Method, Path, RawQuery, Auth, ContentType string
	Body                                      []byte
}

type party struct {
	srv *httptest.Server
	mu  sync.Mutex
	req []recReq
	h   func(w http.ResponseWriter, r *http.Request, body []byte)
}

func newParty(t *testing.T, h func(w http.ResponseWriter, r *http.Request, body []byte)) *party {
	t.Helper()
	p := &party{h: h}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		p.mu.Lock()
		p.req = append(p.req, recReq{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization"), r.Header.Get("Content-Type"), body})
		p.mu.Unlock()
		p.h(w, r, body)
	}))
	t.Cleanup(p.srv.Close)
	return p
}

func (p *party) url() string { return p.srv.URL }

func (p *party) requests() []recReq {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]recReq(nil), p.req...)
}

func (p *party) only(t *testing.T) recReq {
	t.Helper()
	r := p.requests()
	if len(r) != 1 {
		t.Fatalf("want exactly one request to %s, got %d: %+v", p.url(), len(r), r)
	}
	return r[0]
}

func okEmpty(w http.ResponseWriter, _ *http.Request, _ []byte) { w.WriteHeader(204) }

func failWith(status int, msg string) func(http.ResponseWriter, *http.Request, []byte) {
	return func(w http.ResponseWriter, _ *http.Request, _ []byte) { http.Error(w, msg, status) }
}

func jsonReply(v any) func(http.ResponseWriter, *http.Request, []byte) {
	return func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
}

func urls(ps ...*party) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.url()
	}
	return out
}

func optsFor(ps ...*party) Options { return opts(urls(ps...)...) }
