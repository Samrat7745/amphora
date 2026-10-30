package client

// Port of AmphoraServiceUriTest: service address validation and the resource URIs the client
// composes from a service address (the Java AmphoraServiceUri class has no Go counterpart; the
// URIs are observable as the request targets of the client calls).

import (
	"context"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"testing"

	"github.com/google/uuid"
)

func TestServiceAddressValidation(t *testing.T) {
	// Java: "serviceAddress must not be empty!" for null/empty and INVALID_SERVICE_ADDRESS_EXCEPTION_MSG
	// for addresses without scheme/host. The Go client rejects the same inputs.
	for _, a := range []string{"", "   ", "localhost:8080", "invalidUri", "https://", "/only/a/path"} {
		if _, err := New(Options{ServiceAddresses: []string{a}}); err == nil {
			t.Errorf("New(%q) must fail", a)
		}
		if _, err := NewIntraVcpClient(Options{ServiceAddresses: []string{a}}); err == nil {
			t.Errorf("NewIntraVcpClient(%q) must fail", a)
		}
		if _, err := NewInterVcpClient(Options{ServiceAddresses: []string{a}}); err == nil {
			t.Errorf("NewInterVcpClient(%q) must fail", a)
		}
	}
	if _, err := New(Options{ServiceAddresses: []string{"https://ok.example", ""}}); err == nil {
		t.Error("one invalid address among valid ones must fail")
	}
}

func TestServiceAddressForms(t *testing.T) {
	// givenUriStringWith{Domain,IpAndPort,WithoutPort,Path,PortAndPath}: all valid and normalised
	// (a trailing slash is dropped, scheme/host/port/path are kept).
	cases := map[string]string{
		"https://amphora.carbynestack.io:8080":             "https://amphora.carbynestack.io:8080",
		"http://127.0.0.1:8080":                            "http://127.0.0.1:8080",
		"https://amphora.carbynestack.io":                  "https://amphora.carbynestack.io",
		"https://amphora.carbynestack.io/myService":        "https://amphora.carbynestack.io/myService",
		"https://amphora.carbynestack.io:8081/my/service":  "https://amphora.carbynestack.io:8081/my/service",
		"https://amphora.carbynestack.io:8081/my/service/": "https://amphora.carbynestack.io:8081/my/service",
		"https://amphora.carbynestack.io:8081/":            "https://amphora.carbynestack.io:8081",
	}
	for in, want := range cases {
		b, err := newBase(Options{ServiceAddresses: []string{in}})
		if err != nil {
			t.Errorf("%s: %v", in, err)
			continue
		}
		if len(b.addrs) != 1 || b.addrs[0] != want {
			t.Errorf("%s: got %v want %s", in, b.addrs, want)
		}
	}
}

type pathRecorder struct {
	mu    sync.Mutex
	paths []string
}

func (p *pathRecorder) add(method, path string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.paths = append(p.paths, method+" "+path)
}

func (p *pathRecorder) sorted() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := append([]string{}, p.paths...)
	sort.Strings(out)
	return out
}

func TestResourceURIs(t *testing.T) {
	sp := params()
	words := []*big.Int{big.NewInt(7)}
	maskODO, _ := json.Marshal(fakeODOs(t, sp, words, 1, false)[0])
	id := uuid.MustParse("82a73814-321c-4261-abcd-27c6c0ebfb26")

	for _, base := range []string{"/my/service", "/my/service/", "", "/"} {
		prefix := base
		for len(prefix) > 0 && prefix[len(prefix)-1] == '/' {
			prefix = prefix[:len(prefix)-1]
		}
		t.Run("base="+base, func(t *testing.T) {
			rec := &pathRecorder{}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				rec.add(r.Method, r.URL.EscapedPath())
				switch {
				case r.URL.Path == prefix+"/input-masks":
					_, _ = w.Write(maskODO)
				case r.URL.Path == prefix+"/secret-shares" && r.Method == "GET":
					_, _ = w.Write([]byte(`{"content":[],"number":0,"size":0,"totalElements":0,"totalPages":1}`))
				case r.URL.Path == prefix+"/secret-shares/"+id.String()+"/tags":
					_, _ = w.Write([]byte(`[]`))
				case r.URL.Path == prefix+"/secret-shares/"+id.String()+"/tags/key":
					_, _ = w.Write([]byte(`{"key":"key","value":"","valueType":"STRING"}`))
				case r.URL.Path == prefix+"/intra-vcp/secret-shares/"+id.String():
					_, _ = w.Write([]byte(`{"secretId":"` + id.String() + `","tags":[],"data":""}`))
				default:
					w.WriteHeader(http.StatusOK)
				}
			}))
			defer srv.Close()
			o := Options{ServiceAddresses: []string{srv.URL + base}, Prime: sp.Prime, R: sp.R, RInv: sp.RInv}
			ctx := context.Background()

			c, err := New(o)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := c.CreateSecret(ctx, Secret{SecretID: id, Data: words}); err != nil { // input-masks + masked-inputs
				t.Fatal(err)
			}
			if _, err := c.GetSecrets(ctx, nil, nil); err != nil { // secret shares
				t.Fatal(err)
			}
			if _, err := c.GetTags(ctx, id); err != nil { // secret share resource tags
				t.Fatal(err)
			}
			if _, err := c.GetTag(ctx, id, "key"); err != nil { // tag resource
				t.Fatal(err)
			}
			if err := c.DeleteSecret(ctx, id); err != nil { // secret share resource
				t.Fatal(err)
			}
			intra, _ := NewIntraVcpClient(o)
			if _, err := intra.UploadSecretShare(ctx, SecretShare{SecretID: id, Data: make([]byte, 32)}); err != nil { // s2s secret share
				t.Fatal(err)
			}
			if _, err := intra.GetSecretShare(ctx, id); err != nil { // s2s secret share resource
				t.Fatal(err)
			}
			inter, _ := NewInterVcpClient(o)
			if err := inter.Open(ctx, MultiplicationExchangeObject{OperationID: id}); err != nil { // inter-vc open interim values
				t.Fatal(err)
			}

			want := []string{
				"DELETE " + prefix + "/secret-shares/" + id.String(),
				"GET " + prefix + "/input-masks",
				"GET " + prefix + "/intra-vcp/secret-shares/" + id.String(),
				"GET " + prefix + "/secret-shares",
				"GET " + prefix + "/secret-shares/" + id.String() + "/tags",
				"GET " + prefix + "/secret-shares/" + id.String() + "/tags/key",
				"POST " + prefix + "/inter-vcp/open",
				"POST " + prefix + "/intra-vcp/secret-shares",
				"POST " + prefix + "/masked-inputs",
			}
			sort.Strings(want)
			got := rec.sorted()
			if len(got) != len(want) {
				t.Fatalf("got %v\nwant %v", got, want)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("got %v\nwant %v", got, want)
				}
			}
		})
	}
}

func TestPathParameterIsAppendedToBase(t *testing.T) {
	// givenValidPathSegments_whenBuildingResourceUri_thenReturnExpectedUri
	if got := tagPath(uuid.MustParse("82a73814-321c-4261-abcd-27c6c0ebfb26"), "1234"); got != "/secret-shares/82a73814-321c-4261-abcd-27c6c0ebfb26/tags/1234" {
		t.Fatal(got)
	}
}
