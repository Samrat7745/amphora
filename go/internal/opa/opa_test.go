package opa

// Ports of OpaClientTest, OpaResultTest and OpaServiceTest against an httptest OPA.

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/Samrat7745/amphora/go/internal/model"
)

const (
	policyPackage        = "play"
	defaultPolicyPackage = "default"
	subject              = "me"
)

var testTags = []model.Tag{
	{Key: "created", Value: "yesterday", ValueType: model.TypeString},
	{Key: "owner", Value: "me", ValueType: model.TypeString},
}

type recordedRequest struct {
	method, path, accept, contentType, body string
}

type fakeOPA struct {
	*httptest.Server
	mu   sync.Mutex
	reqs []recordedRequest
}

// newOPA answers every request with status and body.
func newOPA(t *testing.T, status int, body string) *fakeOPA {
	t.Helper()
	f := &fakeOPA{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.reqs = append(f.reqs, recordedRequest{r.Method, r.URL.Path, r.Header.Get("Accept"), r.Header.Get("Content-Type"), string(b)})
		f.mu.Unlock()
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(f.Close)
	return f
}

func client(t *testing.T, endpoint string) *Client {
	t.Helper()
	c, err := New(endpoint, defaultPolicyPackage, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func withPolicy(tags []model.Tag, pkg string) []model.Tag {
	return append(append([]model.Tag{}, tags...), model.Tag{Key: PolicyPackageTagKey, Value: pkg, ValueType: model.TypeString})
}

func TestEvaluateUsesPolicyPackageAndSendsSubjectAndTags(t *testing.T) { // OpaClientTest.givenValidRequest_whenEvaluate_thenReturnTrue
	f := newOPA(t, 200, `{"result":false}`)
	tags := withPolicy(testTags, policyPackage)
	if _, err := client(t, f.URL).Allowed(context.Background(), subject, Read, tags); err != nil {
		t.Fatal(err)
	}
	if len(f.reqs) != 1 {
		t.Fatal(len(f.reqs))
	}
	r := f.reqs[0]
	if r.method != "POST" || r.path != "/v1/data/play/read" || r.accept != "application/json" || r.contentType != "application/json" {
		t.Fatalf("%+v", r)
	}
	var got struct {
		Input struct {
			Subject string
			Tags    []model.Tag
		}
	}
	if err := json.Unmarshal([]byte(r.body), &got); err != nil {
		t.Fatal(err)
	}
	if got.Input.Subject != subject || len(got.Input.Tags) != 3 || got.Input.Tags[0].Key != "created" || got.Input.Tags[2].Value != policyPackage {
		t.Fatalf("%s", r.body)
	}
	want := `{"input":{"subject":"me","tags":[{"key":"created","value":"yesterday","valueType":"STRING"},` +
		`{"key":"owner","value":"me","valueType":"STRING"},{"key":"accessPolicy","value":"play","valueType":"STRING"}]}}`
	if r.body != want {
		t.Fatalf("\n got %s\nwant %s", r.body, want)
	}
}

func TestPolicyPackageSelection(t *testing.T) { // OpaClientTest default package + OpaServiceTest policy/no policy
	cases := []struct {
		name, wantPath string
		tags           []model.Tag
	}{
		{"noPolicyDefined_useDefaultPackage", "/v1/data/default/read", testTags},
		{"policyDefinedInTag_usePolicyPackage", "/v1/data/play/read", withPolicy(testTags, policyPackage)},
		{"dotsAreMappedToPathSegments", "/v1/data/carbynestack/def/read", withPolicy(testTags, "carbynestack.def")},
		{"firstPolicyTagWins", "/v1/data/play/read", append(withPolicy(testTags, "play"), model.Tag{Key: PolicyPackageTagKey, Value: "other"})},
		{"nilTags", "/v1/data/default/read", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newOPA(t, 200, `{"result":true}`)
			ok, err := client(t, f.URL).Allowed(context.Background(), subject, Read, c.tags)
			if err != nil || !ok {
				t.Fatal(ok, err)
			}
			if f.reqs[0].path != c.wantPath {
				t.Fatal(f.reqs[0].path)
			}
		})
	}
}

func TestEndpointPathIsReplacedLikeURIResolve(t *testing.T) {
	f := newOPA(t, 200, `{"result":true}`)
	if _, err := client(t, f.URL+"/some/prefix/").Allowed(context.Background(), subject, Use, nil); err != nil {
		t.Fatal(err)
	}
	if f.reqs[0].path != "/v1/data/default/use" {
		t.Fatal(f.reqs[0].path)
	}
}

func TestOpaReturnsFalse(t *testing.T) { // givenOpaReturnsFalse_whenEvaluate_thenReturnFalse
	f := newOPA(t, 200, `{}`)
	ok, err := client(t, f.URL).Allowed(context.Background(), subject, Read, testTags)
	if err != nil || ok {
		t.Fatal(ok, err)
	}
}

func TestNoSubjectOrAction(t *testing.T) {
	f := newOPA(t, 200, `{"result":true}`)
	c := client(t, f.URL)
	_, err := c.Allowed(context.Background(), "", Read, testTags)
	if me, ok := err.(*model.Error); !ok || me.Kind != model.KindInternal || me.Msg != "Subject is required to evaluate the policy" {
		t.Fatal(err)
	}
	_, err = c.Allowed(context.Background(), subject, "", testTags)
	if me, ok := err.(*model.Error); !ok || me.Kind != model.KindInternal || me.Msg != "Action is required to evaluate the policy" {
		t.Fatal(err)
	}
	if len(f.reqs) != 0 {
		t.Fatal("OPA must not be called")
	}
}

func TestClientFailureMeansDenied(t *testing.T) { // givenClientThrows_whenEvaluate_thenReturnFalse
	closed := newOPA(t, 200, `{"result":true}`)
	url := closed.URL
	closed.Close()
	cases := map[string]string{
		"connection refused": url,
		"http 500":           newOPA(t, 500, `{"result":true}`).URL,
		"http 404":           newOPA(t, 404, `{"result":true}`).URL,
		"invalid json":       newOPA(t, 200, `not json`).URL,
		"empty body":         newOPA(t, 200, ``).URL,
	}
	for name, u := range cases {
		t.Run(name, func(t *testing.T) {
			ok, err := client(t, u).Allowed(context.Background(), subject, Read, testTags)
			if err != nil || ok {
				t.Fatal(ok, err)
			}
		})
	}
}

func TestOpaResultParsing(t *testing.T) { // OpaResultTest
	cases := []struct {
		body string
		want bool
	}{
		{`{"result": true}`, true},
		{`{"result": false}`, false},
		{`{}`, false},
		{`{"result":true,"decision_id":"x"}`, true},
	}
	for _, c := range cases {
		f := newOPA(t, 200, c.body)
		ok, err := client(t, f.URL).Allowed(context.Background(), subject, Read, nil)
		if err != nil || ok != c.want {
			t.Errorf("%s: %v %v", c.body, ok, err)
		}
	}
}

func TestActionNames(t *testing.T) { // OpaServiceTest.whenCan*_thenUseProperAction
	want := map[string]string{
		Read: "read", Use: "use", Delete: "delete",
		TagCreate: "tag/create", TagRead: "tag/read", TagUpdate: "tag/update", TagDelete: "tag/delete",
	}
	for action, name := range want {
		if action != name {
			t.Errorf("%q != %q", action, name)
		}
		f := newOPA(t, 200, `{"result":true}`)
		if _, err := client(t, f.URL).Allowed(context.Background(), subject, action, testTags); err != nil {
			t.Fatal(err)
		}
		if f.reqs[0].path != "/v1/data/default/"+name {
			t.Errorf("%s: %s", action, f.reqs[0].path)
		}
	}
	if PolicyPackageTagKey != "accessPolicy" || OwnerTagKey != "owner" {
		t.Fatal("reserved tag keys changed")
	}
}
