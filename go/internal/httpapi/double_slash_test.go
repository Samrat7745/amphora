package httpapi

import (
	"net/http/httptest"
	"testing"

	"github.com/carbynestack/amphora-go/internal/jwt"
)

// The Istio "/amphora" prefix rewrite delivers requests as "//input-masks" etc. Spring (the Java service)
// collapses repeated slashes, so the Go service must route them like their single-slash form. Found with the
// Carbyne Stack CLI against a two-cluster deployment (it got 404 for "//input-masks").
func TestRepeatedSlashesRouteLikeSingleSlash(t *testing.T) {
	a := &API{Svc: &fakeBackend{}, JWT: jwt.Reader{Field: "sub"}}
	h := a.Handler()
	for _, p := range []string{"/actuator/health", "/input-masks", "/secret-shares", "/secret-shares/" + sid + "/tags"} {
		want := httptest.NewRecorder()
		h.ServeHTTP(want, httptest.NewRequest("GET", p, nil))
		for _, slashed := range []string{"/" + p, "//" + p, p + "/", "/" + p + "/"} {
			r := httptest.NewRequest("GET", "/", nil)
			r.URL.Path, r.URL.RawPath = slashed, ""
			got := httptest.NewRecorder()
			h.ServeHTTP(got, r)
			if got.Code != want.Code {
				t.Errorf("GET %q: status %d, want %d (same as %q)", slashed, got.Code, want.Code, p)
			}
		}
	}
}
