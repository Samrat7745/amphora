package opa

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/carbynestack/amphora-go/internal/model"
)

// Failure modes of the OPA server. Every transport or protocol failure means "denied" (false, nil) as in Java's
// OpaClient; only missing subject/action are errors. None of the cases may hang: contexts are short.

func allowedWithin(t *testing.T, c *Client, d time.Duration) (bool, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	done := make(chan struct{})
	var ok bool
	var err error
	go func() { ok, err = c.Allowed(ctx, subject, Read, testTags); close(done) }()
	select {
	case <-done:
		return ok, err
	case <-time.After(d + 5*time.Second):
		t.Fatal("Allowed did not return (request hangs)")
		return false, nil
	}
}

func discardClient(t *testing.T, endpoint string) *Client {
	t.Helper()
	c, err := New(endpoint, defaultPolicyPackage, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestServerFailuresMeanDenied(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"500", 500, `{"result":true}`, false},
		{"503", 503, ``, false},
		{"404 undefined policy", 404, `{"code":"resource_not_found"}`, false},
		{"401", 401, `{"result":true}`, false},
		{"301", 301, `{"result":true}`, false},
		{"malformed json", 200, `{"result":tru`, false},
		{"html", 200, `<html></html>`, false},
		{"empty body", 200, ``, false},
		{"empty object (undefined result)", 200, `{}`, false},
		{"null result", 200, `{"result":null}`, false},
		{"result is a string", 200, `{"result":"true"}`, false},
		{"result is a number", 200, `{"result":1}`, false},
		{"result is an object", 200, `{"result":{"allow":true}}`, false},
		{"array", 200, `[true]`, false},
		{"true", 200, `{"result":true}`, true},
		{"true with extra fields", 200, `{"decision_id":"x","result":true,"metrics":{}}`, true},
		{"false", 200, `{"result":false}`, false},
		{"204 empty", 204, ``, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newOPA(t, tc.status, tc.body)
			defer srv.Close()
			ok, err := allowedWithin(t, discardClient(t, srv.URL), 2*time.Second)
			if err != nil || ok != tc.want {
				t.Fatalf("got (%v, %v), want (%v, nil)", ok, err, tc.want)
			}
		})
	}
}

func TestSlowServerTimesOutViaContext(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)
	start := time.Now()
	ok, err := allowedWithin(t, discardClient(t, srv.URL), 150*time.Millisecond)
	if err != nil || ok {
		t.Fatalf("got (%v, %v)", ok, err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("took %v", time.Since(start))
	}
}

func TestSlowBodyTimesOutViaContext(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		w.WriteHeader(200)
		_, _ = io.WriteString(w, `{"result":`)
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)
	ok, err := allowedWithin(t, discardClient(t, srv.URL), 150*time.Millisecond)
	if err != nil || ok {
		t.Fatalf("got (%v, %v)", ok, err)
	}
}

func TestConnectionRefusedAndReset(t *testing.T) {
	// refused: a port nobody listens on
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	ok, err := allowedWithin(t, discardClient(t, "http://"+addr), 2*time.Second)
	if err != nil || ok {
		t.Fatalf("refused: (%v, %v)", ok, err)
	}
	// reset: the server accepts and closes immediately
	l, err = net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	ok, err = allowedWithin(t, discardClient(t, "http://"+l.Addr().String()), 2*time.Second)
	if err != nil || ok {
		t.Fatalf("reset: (%v, %v)", ok, err)
	}
	// unresolvable host
	ok, err = allowedWithin(t, discardClient(t, "http://opa.invalid:8181"), 2*time.Second)
	if err != nil || ok {
		t.Fatalf("dns: (%v, %v)", ok, err)
	}
}

func TestCancelledContextIsDenied(t *testing.T) {
	srv := newOPA(t, 200, `{"result":true}`)
	defer srv.Close()
	c := discardClient(t, srv.URL)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if ok, err := c.Allowed(ctx, subject, Read, nil); ok || err != nil {
		t.Fatalf("%v %v", ok, err)
	}
	if len(srv.reqs) != 0 {
		t.Fatal("cancelled context must not reach the server")
	}
}

func TestMissingSubjectOrActionAreInternalErrors(t *testing.T) {
	c := discardClient(t, "http://127.0.0.1:1")
	_, err := c.Allowed(context.Background(), "", Read, nil)
	wantInternal(t, err, "Subject is required to evaluate the policy")
	_, err = c.Allowed(context.Background(), subject, "", nil)
	wantInternal(t, err, "Action is required to evaluate the policy")
}

func wantInternal(t *testing.T, err error, msg string) {
	t.Helper()
	me, ok := err.(*model.Error)
	if !ok || me.Kind != model.KindInternal || me.Msg != msg {
		t.Fatalf("got %v", err)
	}
}

func TestNewRejectsBadEndpoint(t *testing.T) {
	if _, err := New("http://[::1", "p", nil); err == nil {
		t.Fatal("invalid endpoint accepted")
	}
	if c, err := New("http://opa:8181", "p", nil); err != nil || c == nil {
		t.Fatal(err)
	}
}

func TestConcurrentRequests(t *testing.T) {
	srv := newOPA(t, 200, `{"result":true}`)
	defer srv.Close()
	c := discardClient(t, srv.URL)
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		go func() {
			ok, err := c.Allowed(context.Background(), subject, Delete, testTags)
			if err != nil || !ok {
				errs <- io.ErrUnexpectedEOF
				return
			}
			errs <- nil
		}()
	}
	for i := 0; i < 20; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
}
