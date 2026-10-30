package httpx

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewClientTrustedCertificates(t *testing.T) {
	if _, err := NewClient(false, []string{filepath.Join(t.TempDir(), "missing.pem")}, time.Second); err == nil {
		t.Fatal("missing certificate file must fail")
	}
	bad := filepath.Join(t.TempDir(), "bad.pem")
	if err := os.WriteFile(bad, []byte("not a pem"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewClient(false, []string{bad}, time.Second); err == nil {
		t.Fatal("file without certificates must fail")
	}
	// with NO_SSL_VALIDATION the trusted certificates are ignored
	if _, err := NewClient(true, []string{bad}, time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestNoSSLValidation(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	strict, _ := NewClient(false, nil, 5*time.Second)
	if _, err := strict.Get(srv.URL); err == nil {
		t.Fatal("self-signed certificate must be rejected")
	}
	lax, _ := NewClient(true, nil, 5*time.Second)
	resp, err := lax.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
}

func TestTrustedCertificate(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	pemFile := filepath.Join(t.TempDir(), "ca.pem")
	crt := srv.Certificate()
	pem := "-----BEGIN CERTIFICATE-----\n" + wrap(crt.Raw) + "-----END CERTIFICATE-----\n"
	if err := os.WriteFile(pemFile, []byte(pem), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := NewClient(false, []string{pemFile}, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
}

func wrap(der []byte) string {
	s := base64.StdEncoding.EncodeToString(der)
	var out string
	for len(s) > 64 {
		out += s[:64] + "\n"
		s = s[64:]
	}
	return out + s + "\n"
}
