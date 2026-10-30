package httpapi

// Table tests for the branches the controller ports do not reach: method/route matrix, malformed query
// parameters, missing authorization, content types, error mapping, panic recovery and the actuator endpoints.

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/carbynestack/amphora-go/internal/jwt"
	"github.com/carbynestack/amphora-go/internal/model"
	"github.com/carbynestack/amphora-go/internal/pgstore"
)

const sid = testSecretID

func TestRouteMethodMatrix(t *testing.T) {
	routes := []struct {
		path    string
		allowed []string
	}{
		{"/actuator/health", []string{"GET"}},
		{"/actuator/info", []string{"GET"}},
		{"/actuator/prometheus", []string{"GET"}},
		{"/secret-shares", []string{"GET"}},
		{"/secret-shares/" + sid, []string{"GET", "DELETE"}},
		{"/secret-shares/" + sid + "/tags", []string{"GET", "POST", "PUT"}},
		{"/secret-shares/" + sid + "/tags/k", []string{"GET", "PUT", "DELETE"}},
		{"/masked-inputs", []string{"POST"}},
		{"/input-masks", []string{"GET"}},
		{"/intra-vcp/secret-shares", []string{"POST"}},
		{"/intra-vcp/secret-shares/" + sid, []string{"GET"}},
		{"/inter-vcp/open", []string{"POST"}},
	}
	all := []string{"GET", "POST", "PUT", "DELETE", "PATCH", "OPTIONS"}
	for _, rt := range routes {
		for _, m := range all {
			isAllowed := false
			for _, a := range rt.allowed {
				isAllowed = isAllowed || a == m
			}
			if isAllowed {
				continue
			}
			t.Run(m+" "+rt.path, func(t *testing.T) {
				f := &fakeBackend{}
				r := call(newAPI(f, &fakeODO{}), m, rt.path, "", authed())
				if r.code != http.StatusMethodNotAllowed {
					t.Fatalf("got %d, want 405", r.code)
				}
				if got := r.hdr.Get("Allow"); got != strings.Join(rt.allowed, ", ") {
					t.Fatalf("Allow=%q", got)
				}
				if len(f.calls) != 0 {
					t.Fatalf("backend called: %v", f.calls)
				}
			})
		}
	}
}

func TestUnknownRoutesAre404(t *testing.T) {
	for _, p := range []string{"/", "/nope", "/secret-shares/" + sid + "/tags/k/extra", "/secret-shares/" + sid + "/other", "/actuator",
		"/actuator/health/liveness", "/intra-vcp", "/inter-vcp/close", "/masked-inputs/x", "/input-masks/x/y", "//", "/%00"} {
		for _, m := range []string{"GET", "POST", "DELETE"} {
			r := call(newAPI(&fakeBackend{}, &fakeODO{}), m, p, "", authed())
			if r.code != 404 || !strings.Contains(r.body, `"status":404`) || r.hdr.Get("Content-Type") != "application/json" {
				t.Fatalf("%s %s: %d %q %v", m, p, r.code, r.body, r.hdr)
			}
		}
	}
}

func TestTrailingSlashAndEscapedSegments(t *testing.T) {
	f := &fakeBackend{tags: []model.Tag{}}
	h := newAPI(f, &fakeODO{})
	wantResp(t, call(h, "GET", "/secret-shares/"+sid+"/tags/", "", authed()), 200, "[]")
	f.tag = model.Tag{Key: "a b", Value: "v", ValueType: model.TypeString}
	if r := call(h, "GET", "/secret-shares/"+sid+"/tags/a%20b", "", authed()); r.code != 200 || f.argsOf("RetrieveTag")[1] != "a b" {
		t.Fatalf("%v %v", r, f.argsOf("RetrieveTag"))
	}
}

func TestListQueryParameters(t *testing.T) {
	bad := []string{
		"pageNumber=abc", "pageSize=abc", "pageNumber=1.5", "pageSize=99999999999", "pageNumber=--1", "pageSize=1_0", "pageNumber=0x", "pageSize=%20x",
	}
	for _, q := range bad {
		f := &fakeBackend{}
		wantResp(t, call(newAPI(f, nil), "GET", "/secret-shares?"+q, "", nil), 400, "")
		if f.called("List") {
			t.Fatalf("%s: backend called", q)
		}
	}
	badFilters := []string{"filter=a", "filter=a:", "filter=:b", "filter=a=b", "filter=a:b:c", "filter=a%25zz:b", "filter=a<x", "filter=a>1.5", "filter=a!:b", "filter=a:b,c"}
	for _, q := range badFilters {
		f := &fakeBackend{}
		r := call(newAPI(f, nil), "GET", "/secret-shares?"+q, "", nil)
		if r.code != 400 || r.body == "" || f.called("List") {
			t.Fatalf("%s: %d %q", q, r.code, r.body)
		}
	}
	type want struct {
		sort   string
		desc   bool
		paged  bool
		page   int
		size   int
		nFiltr int
	}
	ok := map[string]want{
		"":                                  {},
		"sort=k":                            {sort: "k"},
		"sort=k&dir=desc":                   {sort: "k", desc: true},
		"sort=k&dir=DESC":                   {sort: "k", desc: true},
		"sort=k&dir=sideways":               {sort: "k"},
		"dir=desc":                          {}, // dir without sort is ignored
		"sort=%20":                          {}, // blank sort
		"pageNumber=2":                      {paged: true, page: 2, size: 1},
		"pageSize=5":                        {paged: true, page: 0, size: 5},
		"pageNumber=-3&pageSize=4":          {paged: true, page: 0, size: 4},
		"pageSize=-4&pageNumber=1":          {paged: true, page: 1, size: 1},
		"pageNumber=0&pageSize=0":           {},
		"pageNumber=&pageSize=":             {},
		"pageNumber=0x10&pageSize=%2B2":     {paged: true, page: 16, size: 2},
		"filter=a:b,c%3E5":                  {nFiltr: 2},
		"filter=":                           {},
		"filter=%20":                        {},
		"filter=a:b&filter=c:d":             {nFiltr: 2},
		"sort=k&pageSize=2147483647":        {sort: "k", paged: true, size: 2147483647},
		"unknown=1&pageNumber=1&pageSize=3": {paged: true, page: 1, size: 3},
	}
	for q, w := range ok {
		f := &fakeBackend{}
		r := call(newAPI(f, nil), "GET", "/secret-shares?"+q, "", nil)
		if r.code != 200 {
			t.Fatalf("%q: %d %q", q, r.code, r.body)
		}
		p := f.argsOf("List")[0].(pgstore.ListParams)
		if p.SortKey != w.sort || p.SortDesc != w.desc || p.Paged != w.paged || p.Page != w.page || p.PageSize != w.size || len(p.Filters) != w.nFiltr {
			t.Fatalf("%q: %+v", q, p)
		}
	}
}

func TestInputMaskQueryParameters(t *testing.T) {
	const rid = "7520e090-1437-44da-9e4e-eea5b2200fea"
	for _, q := range []string{"count=x", "count=", "count=1.5", "count=99999999999999999999", "count=0x", "count=-0x1x"} {
		f := &fakeBackend{}
		wantResp(t, call(newAPI(f, nil), "GET", "/input-masks?requestId="+rid+"&"+q, "", nil), 400, "")
		if f.called("InputMasks") {
			t.Fatal(q)
		}
	}
	for _, q := range []string{"count=0", "count=-5", "count=-0x5"} {
		wantResp(t, call(newAPI(&fakeBackend{}, nil), "GET", "/input-masks?requestId="+rid+"&"+q, "", nil), 400, `"The number of requested Input Masks has to be 1 or greater."`)
	}
	f := &fakeBackend{}
	wantResp(t, call(newAPI(f, nil), "GET", "/input-masks?requestId="+rid+"&count=0x10", "", nil), 200, "*")
	if f.argsOf("InputMasks")[1] != int64(16) {
		t.Fatal(f.argsOf("InputMasks"))
	}
	wantResp(t, call(newAPI(&fakeBackend{}, nil), "GET", "/input-masks?count=1", "", nil), 400, "")
}

func TestMissingOrBadAuthorization(t *testing.T) {
	type rq struct{ method, path, body string }
	tag := `{"key":"k","value":"v","valueType":"STRING"}`
	reqs := []rq{
		{"GET", "/secret-shares/" + sid + "?requestId=" + testRequestID, ""},
		{"DELETE", "/secret-shares/" + sid, ""},
		{"GET", "/secret-shares/" + sid + "/tags", ""},
		{"POST", "/secret-shares/" + sid + "/tags", tag},
		{"PUT", "/secret-shares/" + sid + "/tags", "[" + tag + "]"},
		{"GET", "/secret-shares/" + sid + "/tags/k", ""},
		{"PUT", "/secret-shares/" + sid + "/tags/k", tag},
		{"DELETE", "/secret-shares/" + sid + "/tags/k", ""},
		{"POST", "/masked-inputs", `{"secretId":"` + sid + `","data":[{"value":"AAAAAAAAAAAAAAAAAAAAAA=="}]}`},
	}
	for _, q := range reqs {
		t.Run(q.method+" "+q.path, func(t *testing.T) {
			f := &fakeBackend{}
			h := newAPI(f, &fakeODO{})
			// no header at all: Spring's missing @RequestHeader -> 400 with empty body
			wantResp(t, call(h, q.method, q.path, q.body, nil), 400, "")
			// header present but not a bearer token / not a JWT -> 401
			for _, hv := range []string{"", "Basic abc", "Bearer", "Bearer a.b", "bearer a.e30=.c", "Bearer a." + "e30" + ".c"} {
				r := call(h, q.method, q.path, q.body, map[string]string{"Authorization": hv})
				if r.code != 401 || r.body != `"No token provided"` {
					t.Fatalf("%q: %d %q", hv, r.code, r.body)
				}
			}
			// not base64 -> 400 with the Java message
			r := call(h, q.method, q.path, q.body, map[string]string{"Authorization": "Bearer a.@@.c"})
			if r.code != 400 || !strings.Contains(r.body, "Illegal base64 character") {
				t.Fatalf("%d %q", r.code, r.body)
			}
			if len(f.calls) != 0 {
				t.Fatalf("backend called: %v", f.calls)
			}
		})
	}
}

func TestContentTypeHandling(t *testing.T) {
	tag := `{"key":"k","value":"v","valueType":"STRING"}`
	path := "/secret-shares/" + sid + "/tags"
	cases := []struct {
		ct   string
		want int
	}{
		{"application/json", 201},
		{"application/json;charset=UTF-8", 201},
		{"Application/JSON", 201},
		{"application/vnd.api+json", 201},
		{"application/problem+json; charset=utf-8", 201},
		{"text/plain", 415},
		{"application/x-www-form-urlencoded", 415},
		{"application/xml", 415},
		{"application/", 415},
		{"json", 415},
		{";;;", 415},
		{"", 415}, // httptest helper below removes the header entirely
	}
	for _, c := range cases {
		t.Run(c.ct, func(t *testing.T) {
			f := &fakeBackend{}
			rq := httptest.NewRequest("POST", path, strings.NewReader(tag))
			rq.Header.Set("Authorization", authHeaderValue)
			if c.ct != "" {
				rq.Header.Set("Content-Type", c.ct)
			}
			w := httptest.NewRecorder()
			newAPI(f, nil).ServeHTTP(w, rq)
			if w.Code != c.want {
				t.Fatalf("%d %q", w.Code, w.Body.String())
			}
			if c.want == 415 && (w.Body.Len() != 0 || len(f.calls) != 0) {
				t.Fatal("415 must have an empty body and not reach the backend")
			}
		})
	}
	// empty and whitespace only bodies are rejected before parsing
	for _, b := range []string{"", "  \n\t"} {
		rq := httptest.NewRequest("POST", path, strings.NewReader(b))
		rq.Header.Set("Authorization", authHeaderValue)
		rq.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		newAPI(&fakeBackend{}, nil).ServeHTTP(w, rq)
		if w.Code != 400 || w.Body.Len() != 0 {
			t.Fatalf("%d %q", w.Code, w.Body.String())
		}
	}
}

func TestMalformedBodies(t *testing.T) {
	endpoints := []struct{ method, path string }{
		{"POST", "/secret-shares/" + sid + "/tags"},
		{"PUT", "/secret-shares/" + sid + "/tags"},
		{"PUT", "/secret-shares/" + sid + "/tags/k"},
		{"POST", "/masked-inputs"},
		{"POST", "/intra-vcp/secret-shares"},
		{"POST", "/inter-vcp/open"},
	}
	bodies := []string{`{`, `[`, `null`, `"x"`, `123`, `{"key":`, "\x00\xff", strings.Repeat("[", 3000), `{"secretId":1,"data":3}`, `{"interimValues":[{"a":1e99999999}]}`}
	for _, e := range endpoints {
		for _, b := range bodies {
			if e.path == "/inter-vcp/open" && strings.HasPrefix(b, `{"secretId":1`) {
				continue // unknown properties are ignored; an empty exchange object is valid
			}
			f := &fakeBackend{}
			r := call(newAPI(f, nil), e.method, e.path, b, authed())
			if r.code < 400 || r.code >= 500 {
				t.Fatalf("%s %s %q: got %d (want a 4xx)", e.method, e.path, b, r.code)
			}
		}
	}
}

func TestPathIDMustBeUUID(t *testing.T) {
	for _, id := range []string{"x", "123", "zzzzzzzz-8f50-4d24-a37b-b0075bb5e779", "3bcf8308-8f50-4d24-a37b-b0075bb5e779-1"} {
		f := &fakeBackend{}
		h := newAPI(f, &fakeODO{})
		for _, rq := range [][2]string{{"GET", "/secret-shares/" + id + "?requestId=" + testRequestID}, {"DELETE", "/secret-shares/" + id},
			{"GET", "/secret-shares/" + id + "/tags"}, {"GET", "/intra-vcp/secret-shares/" + id + "?programId=p"}} {
			wantResp(t, call(h, rq[0], rq[1], "", authed()), 400, "")
		}
		if len(f.calls) != 0 {
			t.Fatal(f.calls)
		}
	}
}

// ---- error mapping ----

type errBackend struct {
	fakeBackend
	err error
}

func (e *errBackend) List(context.Context, pgstore.ListParams) (model.MetadataPage, error) {
	return model.MetadataPage{}, e.err
}
func (e *errBackend) GetSecretShare(context.Context, string, string) (*model.SecretShare, error) {
	return nil, e.err
}
func (e *errBackend) DeleteSecret(context.Context, string, string) error { return e.err }
func (e *errBackend) RetrieveTags(context.Context, string, string) ([]model.Tag, error) {
	return nil, e.err
}
func (e *errBackend) StoreTag(context.Context, string, model.Tag, string) (string, error) {
	return "", e.err
}
func (e *errBackend) ReplaceTags(context.Context, string, []model.Tag, string) error { return e.err }
func (e *errBackend) RetrieveTag(context.Context, string, string, string) (model.Tag, error) {
	return model.Tag{}, e.err
}
func (e *errBackend) UpdateTag(context.Context, string, model.Tag, string) error { return e.err }
func (e *errBackend) DeleteTag(context.Context, string, string, string) error    { return e.err }
func (e *errBackend) CreateSecret(context.Context, *model.MaskedInput, string) (string, error) {
	return "", e.err
}
func (e *errBackend) InputMasks(context.Context, string, int64) (model.OutputDeliveryObject, error) {
	return model.OutputDeliveryObject{}, e.err
}
func (e *errBackend) StoreSecretShare(context.Context, *model.SecretShare) (string, error) {
	return "", e.err
}
func (e *errBackend) UseSecretShare(context.Context, string, string) (*model.SecretShare, error) {
	return nil, e.err
}
func (e *errBackend) PutInterim(context.Context, model.ExchangeObject) error { return e.err }

type errODO struct{ err error }

func (e errODO) ComputeFromSecretShareData(context.Context, []byte, string) (model.OutputDeliveryObject, error) {
	return model.OutputDeliveryObject{}, e.err
}

func allEndpoints() []struct{ name, method, path, body string } {
	tag := `{"key":"k","value":"v","valueType":"STRING"}`
	mi := `{"secretId":"` + sid + `","data":[{"value":"AAAAAAAAAAAAAAAAAAAAAA=="}]}`
	share := `{"secretId":"` + sid + `","tags":[],"data":"` + base64.StdEncoding.EncodeToString(make([]byte, model.SecretShareWidth)) + `"}`
	return []struct{ name, method, path, body string }{
		{"list", "GET", "/secret-shares", ""},
		{"get", "GET", "/secret-shares/" + sid + "?requestId=" + testRequestID, ""},
		{"delete", "DELETE", "/secret-shares/" + sid, ""},
		{"getTags", "GET", "/secret-shares/" + sid + "/tags", ""},
		{"createTag", "POST", "/secret-shares/" + sid + "/tags", tag},
		{"replaceTags", "PUT", "/secret-shares/" + sid + "/tags", "[" + tag + "]"},
		{"getTag", "GET", "/secret-shares/" + sid + "/tags/k", ""},
		{"putTag", "PUT", "/secret-shares/" + sid + "/tags/k", tag},
		{"deleteTag", "DELETE", "/secret-shares/" + sid + "/tags/k", ""},
		{"maskedInput", "POST", "/masked-inputs", mi},
		{"inputMasks", "GET", "/input-masks?requestId=" + testRequestID + "&count=1", ""},
		{"intraUpload", "POST", "/intra-vcp/secret-shares", share},
		{"intraDownload", "GET", "/intra-vcp/secret-shares/" + sid + "?programId=p", ""},
		{"open", "POST", "/inter-vcp/open", `{"operationId":"` + sid + `","playerId":1,"interimValues":[]}`},
	}
}

func TestServiceErrorMapping(t *testing.T) {
	errs := []struct {
		name string
		err  error
		code int
		body string
		ct   string
	}{
		{"not found", model.NotFound("No secret with the given id #%s exists.", sid), 404, `"No secret with the given id #` + sid + ` exists."`, "text/plain;charset=UTF-8"},
		{"already exists", model.AlreadyExists("A secret with the given id already exists."), 409, `"A secret with the given id already exists."`, "text/plain;charset=UTF-8"},
		{"bad request", model.BadRequest("Two or more tags with the same key defined."), 400, `"Two or more tags with the same key defined."`, "text/plain;charset=UTF-8"},
		{"unauthorized", model.Unauthorized("User is not authorized to read this secret"), 401, `"User is not authorized to read this secret"`, "text/plain;charset=UTF-8"},
		{"binding", model.Binding("x"), 400, "", ""},
		{"internal", model.Internal("boom \"quoted\""), 500, `"boom \"quoted\""`, "text/plain;charset=UTF-8"},
		{"NPE", model.NPE(), 500, "null", "text/plain;charset=UTF-8"},
		{"plain error", errors.New("plain"), 500, `"plain"`, "text/plain;charset=UTF-8"},
		{"wrapped", errors.Join(errors.New("ctx"), model.NotFound("gone")), 404, `"gone"`, "text/plain;charset=UTF-8"},
	}
	for _, e := range errs {
		for _, ep := range allEndpoints() {
			t.Run(e.name+"/"+ep.name, func(t *testing.T) {
				a := &API{Svc: &errBackend{err: e.err}, ODO: &fakeODO{}, JWT: jwt.Reader{Field: "sub"}}
				r := call(a.Handler(), ep.method, ep.path, ep.body, authed())
				if r.code != e.code || r.body != e.body {
					t.Fatalf("got %d %q want %d %q", r.code, r.body, e.code, e.body)
				}
				if e.ct != "" && r.hdr.Get("Content-Type") != e.ct {
					t.Fatalf("content type %q", r.hdr.Get("Content-Type"))
				}
			})
		}
	}
}

func TestODOErrorMapping(t *testing.T) {
	for _, e := range []struct {
		err  error
		code int
	}{{model.Internal("Failed to retrieve the required Tuples form Castor"), 500}, {model.BadRequest("bad"), 400}, {errors.New("x"), 500}} {
		f := &fakeBackend{secret: &model.SecretShare{Metadata: model.Metadata{SecretID: sid}}}
		a := &API{Svc: f, ODO: errODO{e.err}, JWT: jwt.Reader{Field: "sub"}}
		r := call(a.Handler(), "GET", "/secret-shares/"+sid+"?requestId="+testRequestID, "", authed())
		if r.code != e.code {
			t.Fatalf("%d %q", r.code, r.body)
		}
	}
}

// ---- panics ----

type panicBackend struct{ fakeBackend }

func (panicBackend) List(context.Context, pgstore.ListParams) (model.MetadataPage, error) {
	panic("kaboom")
}
func (panicBackend) RetrieveTags(context.Context, string, string) ([]model.Tag, error) {
	var m map[string]int
	m["x"] = 1 // nil map write
	return nil, nil
}

func TestPanicsAreRecoveredAsInternalError(t *testing.T) {
	a := &API{Svc: &panicBackend{}, JWT: jwt.Reader{Field: "sub"}}
	h := a.Handler()
	r := call(h, "GET", "/secret-shares", "", nil)
	if r.code != 500 || r.body != `"kaboom"` {
		t.Fatalf("%d %q", r.code, r.body)
	}
	r = call(h, "GET", "/secret-shares/"+sid+"/tags", "", authed())
	if r.code != 500 {
		t.Fatalf("%d %q", r.code, r.body)
	}
	// the server keeps working afterwards and the failure is visible in the metrics
	wantResp(t, call(h, "GET", "/actuator/info", "", nil), 200, "{}")
	m := call(h, "GET", "/actuator/prometheus", "", nil).body
	if !strings.Contains(m, `outcome="SERVER_ERROR"`) || !strings.Contains(m, `status="500"`) {
		t.Fatal("500 not recorded in metrics")
	}
	// the handler can also be exercised through a real server: the client must see a 500, not a dropped connection
	srv := httptest.NewServer(h)
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/secret-shares")
	if err != nil || resp.StatusCode != 500 {
		t.Fatalf("%v %v", resp, err)
	}
	_ = resp.Body.Close()
}

// ---- actuator ----

func TestHealthWithEachDependencyDown(t *testing.T) {
	up := func(context.Context) error { return nil }
	down := func(context.Context) error { return errors.New("down") }
	slow := func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }
	cases := []struct {
		name   string
		checks []HealthCheck
		code   int
		body   string
	}{
		{"no checks", nil, 200, `{"status":"UP"}`},
		{"all up", []HealthCheck{up, up, up}, 200, `{"status":"UP"}`},
		{"db down", []HealthCheck{down, up, up}, 503, `{"status":"DOWN"}`},
		{"redis down", []HealthCheck{up, down, up}, 503, `{"status":"DOWN"}`},
		{"minio down", []HealthCheck{up, up, down}, 503, `{"status":"DOWN"}`},
		{"all down", []HealthCheck{down, down}, 503, `{"status":"DOWN"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := &API{Svc: &fakeBackend{}, Checks: c.checks, JWT: jwt.Reader{Field: "sub"}}
			r := call(a.Handler(), "GET", "/actuator/health", "", nil)
			wantResp(t, r, c.code, c.body)
			if r.hdr.Get("Content-Type") != "application/vnd.spring-boot.actuator.v3+json" {
				t.Fatal(r.hdr)
			}
		})
	}
	// a check that never returns is cut off by the request context (no hang)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a := &API{Svc: &fakeBackend{}, Checks: []HealthCheck{slow}, JWT: jwt.Reader{Field: "sub"}}
	rq := httptest.NewRequest("GET", "/actuator/health", nil).WithContext(ctx)
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, rq)
	if w.Code != 503 {
		t.Fatalf("%d", w.Code)
	}
}

func TestInfoEndpoint(t *testing.T) {
	a := &API{Svc: &fakeBackend{}, Info: Info{"version": "1.0", "name": "amphora \"x\"", "apiVersion": "v1", "ignored": "y", "groupId": "g"}}
	wantResp(t, call(a.Handler(), "GET", "/actuator/info", "", nil), 200, `{"name":"amphora \"x\"","groupId":"g","version":"1.0","apiVersion":"v1"}`)
	if r := call(a.Handler(), "HEAD", "/actuator/info", "", nil); r.code != 200 {
		t.Fatal(r.code)
	}
}

func TestPrometheusMetricsLabels(t *testing.T) {
	f := &fakeBackend{tags: []model.Tag{}}
	a := &API{Svc: f, ODO: &fakeODO{}, JWT: jwt.Reader{Field: "sub"}}
	h := a.Handler()
	call(h, "GET", "/secret-shares", "", nil)
	call(h, "GET", "/secret-shares/"+sid+"/tags", "", authed())
	call(h, "GET", "/secret-shares/"+sid+"/tags", "", nil) // 400
	call(h, "POST", "/secret-shares", "", nil)             // 405
	call(h, "GET", "/nope", "", nil)                       // 404
	r := call(h, "GET", "/actuator/prometheus", "", nil)
	if r.code != 200 || r.hdr.Get("Content-Type") != "text/plain;version=0.0.4;charset=utf-8" {
		t.Fatalf("%d %v", r.code, r.hdr)
	}
	for _, want := range []string{
		`http_server_requests_seconds_count{exception="None",method="GET",outcome="SUCCESS",status="200",uri="/secret-shares"} 1`,
		`http_server_requests_seconds_count{exception="None",method="GET",outcome="SUCCESS",status="200",uri="/secret-shares/{secretId}/tags"} 1`,
		`http_server_requests_seconds_count{exception="None",method="GET",outcome="CLIENT_ERROR",status="400",uri="/secret-shares/{secretId}/tags"} 1`,
		`http_server_requests_seconds_count{exception="None",method="POST",outcome="CLIENT_ERROR",status="405",uri="/secret-shares"} 1`,
		`http_server_requests_seconds_count{exception="None",method="GET",outcome="CLIENT_ERROR",status="404",uri="NOT_FOUND"} 1`,
		`http_server_requests_seconds_bucket{exception="None",method="GET",outcome="SUCCESS",status="200",uri="/secret-shares",le="0.01"}`,
		"go_goroutines",
	} {
		if !strings.Contains(r.body, want) {
			t.Fatalf("metrics miss %q", want)
		}
	}
	// the secret id never appears as a label (cardinality)
	if strings.Contains(r.body, `uri="/secret-shares/`+sid) {
		t.Fatal("raw path leaked into the uri label")
	}
}

func TestParseInt32Variants(t *testing.T) {
	good := map[string]int32{"0": 0, "7": 7, "+7": 7, "-7": -7, " 12 ": 12, "0x1F": 31, "0X1f": 31, "#ff": 255, "-0x10": -16, "2147483647": 2147483647, "-2147483648": -2147483648}
	for s, want := range good {
		if got, ok := parseInt32(s); !ok || got != want {
			t.Fatalf("%q: %d %v", s, got, ok)
		}
	}
	for _, s := range []string{"", "-", "+", "0x", "#", "1_0", "--1", "+-1", "2147483648", "-2147483649", "1.0", "abc", "0xzz"} {
		if _, ok := parseInt32(s); ok {
			t.Fatalf("%q accepted", s)
		}
	}
	if n, ok := parseHexLong("-0x10"); !ok || n != -16 {
		t.Fatal(n, ok)
	}
	if _, ok := parseHexLong("10"); ok {
		t.Fatal("decimal must not be parsed as hex")
	}
}
