package client

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// Port of AmphoraCommunicationClientTest: the shared transport of all clients (base.do / base.all)
// issues the requests, never lets one failing service hide the results of the others, and
// reports errors per service.

func newTestBase(t *testing.T, ps ...*party) *base {
	t.Helper()
	b, err := newBase(optsFor(ps...))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// givenSuccessfulRequest_whenFetchingDataFromOnePlayer_thenReturnExpectedContent
func TestCommDownloadFromOnePlayer(t *testing.T) {
	p := newParty(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) { _, _ = w.Write([]byte("response1")) })
	b := newTestBase(t, p)
	r := b.do(context.Background(), "GET", b.addrs[0], "/x", nil, nil, true)
	if r.err != nil || string(r.body) != "response1" || r.status != 200 {
		t.Fatalf("%+v", r)
	}
	if req := p.only(t); req.Method != "GET" || req.Path != "/x" {
		t.Fatalf("%+v", req)
	}
}

// givenHttpClientThrowsException_whenFetchingDataFromOnePlayer_thenForwardException
func TestCommDownloadFromOnePlayerFailureIsForwarded(t *testing.T) {
	p := newParty(t, failWith(500, "Totally expected :O"))
	b := newTestBase(t, p)
	r := b.do(context.Background(), "GET", b.addrs[0], "/x", nil, nil, true)
	if r.err == nil || !strings.Contains(r.err.Error(), "500") || !strings.Contains(r.err.Error(), "Totally expected :O") {
		t.Fatalf("%+v", r)
	}
	// transport errors (service unreachable) are forwarded as well
	b2, _ := newBase(opts("http://127.0.0.1:1"))
	if r := b2.do(context.Background(), "GET", b2.addrs[0], "/x", nil, nil, true); r.err == nil {
		t.Fatal("unreachable service must fail")
	}
}

// givenSuccessfulRequest_whenFetchingDataFromMultiplePlayers_thenReturnExpectedContent
func TestCommDownloadFromMultiplePlayers(t *testing.T) {
	p1 := newParty(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) { _, _ = w.Write([]byte("response1")) })
	p2 := newParty(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) { _, _ = w.Write([]byte("response2")) })
	b := newTestBase(t, p1, p2)
	res, err := b.all(context.Background(), failResponses, "GET", "/x", nil, nil)
	if err != nil || len(res) != 2 || string(res[0].body) != "response1" || string(res[1].body) != "response2" {
		t.Fatalf("%+v %v", res, err)
	}
}

// givenOneRequestFails_whenFetchingDataFromMultiplePlayers_thenReturnExpectedResultMap
func TestCommOneDownloadFailsOthersStillReturned(t *testing.T) {
	p1 := newParty(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) { _, _ = w.Write([]byte("response1")) })
	p2 := newParty(t, failWith(500, "Totally expected :O"))
	b := newTestBase(t, p1, p2)
	res, err := b.all(context.Background(), failResponses, "GET", "/x", nil, nil)
	if err == nil || len(res) != 2 {
		t.Fatalf("aggregate error expected: %v", err)
	}
	if res[0].err != nil || string(res[0].body) != "response1" || res[1].err == nil {
		t.Fatalf("only the second request may fail: %+v", res)
	}
}

// givenSuccessfulRequest_whenUploadingToOnePlayer_thenReturnExpectedContent
func TestCommUploadToOnePlayer(t *testing.T) {
	p := newParty(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) { _, _ = w.Write([]byte("response1")) })
	b := newTestBase(t, p)
	r := b.do(context.Background(), "POST", b.addrs[0], "/x", nil, "body1", true)
	if r.err != nil || string(r.body) != "response1" {
		t.Fatalf("%+v", r)
	}
	if req := p.only(t); string(req.Body) != `"body1"` || req.ContentType != "application/json" {
		t.Fatalf("body must be sent as JSON: %+v", req)
	}
}

// givenHttpClientThrowsException_whenUploadingToOnePlayer_thenForwardException
func TestCommUploadToOnePlayerFailureIsForwarded(t *testing.T) {
	p := newParty(t, failWith(500, "Totally expected :O"))
	b := newTestBase(t, p)
	if r := b.do(context.Background(), "POST", b.addrs[0], "/x", nil, "body1", true); r.err == nil || !strings.Contains(r.err.Error(), "Totally expected :O") {
		t.Fatalf("%+v", r)
	}
}

// givenOneRequestFails_whenUploadingToMultiplePlayers_thenReturnExpectedResultMap: each service gets
// its own body; an empty success response is a success.
func TestCommUploadToMultiplePlayersOneFails(t *testing.T) {
	p1 := newParty(t, okEmpty)
	p2 := newParty(t, failWith(500, "Totally expected :O"))
	b := newTestBase(t, p1, p2)
	r1 := b.do(context.Background(), "POST", b.addrs[0], "/x", nil, "body1", true)
	r2 := b.do(context.Background(), "POST", b.addrs[1], "/x", nil, "body2", true)
	if r1.err != nil || len(r1.body) != 0 {
		t.Fatalf("%+v", r1)
	}
	if r2.err == nil || !strings.Contains(r2.err.Error(), "Totally expected :O") {
		t.Fatalf("%+v", r2)
	}
	if string(p1.only(t).Body) != `"body1"` || string(p2.only(t).Body) != `"body2"` {
		t.Fatal("every service must receive its own body")
	}
	// the same body to all services, as used by the client; all errors are aggregated
	res, err := b.all(context.Background(), failCheck, "POST", "/y", nil, "same")
	if err == nil || res[0].err != nil || res[1].err == nil {
		t.Fatalf("%+v %v", res, err)
	}
	if !strings.Contains(err.Error(), `Request for endpoint "`+p2.url()+`" failed`) || strings.Contains(err.Error(), p1.url()) {
		t.Fatalf("error must only name the failing endpoint: %v", err)
	}
}

// givenOneRequestFails_whenUpdatingOnMultiplePlayers_thenReturnExpectedResultMap
func TestCommUpdateOneFails(t *testing.T) {
	p1 := newParty(t, okEmpty)
	p2 := newParty(t, failWith(500, "Totally expected :O"))
	b := newTestBase(t, p1, p2)
	res, err := b.all(context.Background(), failAtLeastOne, "PUT", "/x", nil, "body")
	failed := 0
	for _, r := range res {
		if r.err != nil {
			failed++
		}
	}
	if err == nil || len(res) != 2 || failed != 1 || res[1].err == nil {
		t.Fatalf("exactly one request should've failed: %+v %v", res, err)
	}
	if p1.only(t).Method != "PUT" {
		t.Fatal("PUT expected")
	}
}

// givenSuccessfulRequest_whenDeletingOnOnePlayer_thenSucceed (and no further interaction)
func TestCommDeleteOnOnePlayer(t *testing.T) {
	p := newParty(t, okEmpty)
	b := newTestBase(t, p)
	if _, err := b.all(context.Background(), failAtLeastOne, "DELETE", "/x", nil, nil); err != nil {
		t.Fatal(err)
	}
	if req := p.only(t); req.Method != "DELETE" || req.Path != "/x" || len(req.Body) != 0 {
		t.Fatalf("%+v", req)
	}
}

// givenRequestFails_whenDeletingOnOnePlayer_thenForwardException
func TestCommDeleteFailureIsForwarded(t *testing.T) {
	p := newParty(t, failWith(404, "Totally expected :O"))
	b := newTestBase(t, p)
	res, err := b.all(context.Background(), failAtLeastOne, "DELETE", "/x", nil, nil)
	if err == nil || res[0].err == nil || !strings.Contains(err.Error(), "Totally expected :O") {
		t.Fatalf("%+v %v", res, err)
	}
}

// givenSuccessfulRequest_whenDeletingFromMultiplePlayer_thenSucceed and
// givenOneRequestFails_whenDeletingFromMultiplePlayers_thenReturnExpectedResult
func TestCommDeleteOnMultiplePlayers(t *testing.T) {
	p1, p2 := newParty(t, okEmpty), newParty(t, okEmpty)
	b := newTestBase(t, p1, p2)
	if _, err := b.all(context.Background(), failAtLeastOne, "DELETE", "/x", nil, nil); err != nil {
		t.Fatal(err)
	}
	p1.only(t)
	p2.only(t)

	q1, q2 := newParty(t, okEmpty), newParty(t, failWith(500, "Totally expected :O"))
	b = newTestBase(t, q1, q2)
	res, err := b.all(context.Background(), failAtLeastOne, "DELETE", "/x", nil, nil)
	if err == nil || len(res) != 2 || res[0].err != nil || res[1].err == nil {
		t.Fatalf("%+v %v", res, err)
	}
	q1.only(t)
	q2.only(t)
}

// the bearer token is requested per service and attached to every user facing request
func TestCommBearerTokenPerService(t *testing.T) {
	p1, p2 := newParty(t, okEmpty), newParty(t, okEmpty)
	o := optsFor(p1, p2)
	o.BearerTokenProvider = func(addr string) string { return "token-for-" + addr }
	b, err := newBase(o)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.all(context.Background(), failCheck, "GET", "/x", nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := p1.only(t).Auth; got != "Bearer token-for-"+p1.url() {
		t.Fatalf("%q", got)
	}
	if got := p2.only(t).Auth; got != "Bearer token-for-"+p2.url() {
		t.Fatalf("%q", got)
	}
	// without provider no Authorization header is sent
	p3 := newParty(t, okEmpty)
	b3 := newTestBase(t, p3)
	_ = b3.do(context.Background(), "GET", b3.addrs[0], "/x", nil, nil, true)
	if p3.only(t).Auth != "" {
		t.Fatal("no token without provider")
	}
}

func TestCommErrorKinds(t *testing.T) {
	p := newParty(t, failWith(500, "x"))
	b := newTestBase(t, p)
	_, err := b.all(context.Background(), failCheck, "GET", "/x", nil, nil)
	ce, ok := err.(*ClientError)
	if !ok || !strings.HasPrefix(ce.Msg, CreateFailedPrefix) {
		t.Fatalf("%v", err)
	}
	// JSON helper sanity: the body encoder produces valid JSON for structs
	if raw, _ := json.Marshal(Tag{Key: "k", Value: "v"}); string(raw) != `{"key":"k","value":"v","valueType":"STRING"}` {
		t.Fatalf("%s", raw)
	}
}
