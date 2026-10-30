package castor

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func b64(n int, v byte) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = v
	}
	return base64.StdEncoding.EncodeToString(b)
}

func TestNewRequiresScheme(t *testing.T) {
	for _, u := range []string{"castor.default.svc.cluster.local", "", "localhost:10100"} {
		if _, err := New(u, false, nil); err == nil {
			t.Errorf("%q must be rejected", u)
		}
	}
}

func TestDownload(t *testing.T) {
	var gotURL, gotAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURL, gotAccept = r.URL.String(), r.Header.Get("Accept")
		_, _ = w.Write([]byte(`{"tuples":[{"shares":[{"value":"` + b64(16, 1) + `","mac":"` + b64(16, 2) + `"}]},{"shares":[{"value":"` + b64(16, 3) + `","mac":"` + b64(16, 4) + `"},{"value":"` + b64(16, 5) + `","mac":"` + b64(16, 6) + `"}]}]}`))
	}))
	defer srv.Close()
	c, err := New(srv.URL+"/", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := c.Download(context.Background(), "70297fd4-d412-4dbb-af05-6818fe0e687a", InputMaskGfp, 2)
	if err != nil {
		t.Fatal(err)
	}
	if gotURL != "/intra-vcp/tuples?count=2&reservationId=70297fd4-d412-4dbb-af05-6818fe0e687a&tupletype=INPUT_MASK_GFP" || gotAccept != "application/json" {
		t.Fatal(gotURL, gotAccept)
	}
	if len(res) != 2 || len(res[0]) != 1 || len(res[1]) != 2 || res[0][0].Value[0] != 1 || res[1][1].Mac[0] != 6 {
		t.Fatalf("%+v", res)
	}
}

func TestDownloadFailures(t *testing.T) {
	cases := map[string]struct {
		status int
		body   string
		want   string
	}{
		"http error":      {500, "no tuples", "Request has failed with status <500>: no tuples"},
		"invalid json":    {200, "nope", "GET request failed."},
		"missing mac":     {200, `{"tuples":[{"shares":[{"value":"AA=="}]}]}`, "GET request failed."},
		"bad base64":      {200, `{"tuples":[{"shares":[{"value":"!!","mac":"AA=="}]}]}`, "GET request failed."},
		"length mismatch": {200, `{"tuples":[{"shares":[{"value":"AA==","mac":"AAA="}]}]}`, "GET request failed."},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(c.status)
				_, _ = w.Write([]byte(c.body))
			}))
			defer srv.Close()
			cl, _ := New(srv.URL, false, nil)
			_, err := cl.Download(context.Background(), "x", MultTripleGfp, 1)
			want := "Failed downloading tuples from service " + srv.URL + ": " + c.want
			if err == nil || err.Error() != want {
				t.Fatalf("got %v\nwant %s", err, want)
			}
		})
	}
	cl, _ := New("http://127.0.0.1:1", false, nil)
	if _, err := cl.Download(context.Background(), "x", MultTripleGfp, 1); err == nil || !strings.HasSuffix(err.Error(), "GET request failed.") {
		t.Fatal(err)
	}
}
