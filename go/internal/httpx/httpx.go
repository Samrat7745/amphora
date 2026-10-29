// Package httpx builds HTTP clients honouring NO_SSL_VALIDATION / TRUSTED_CERTIFICATES.
package httpx

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"os"
	"time"
)

// NewClient returns a client; trusted certs are added to the system roots (ignored when
// noSSLValidation is set, like the Java Castor client).
func NewClient(noSSLValidation bool, trustedCerts []string, timeout time.Duration) (*http.Client, error) {
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !noSSLValidation {
		for _, p := range trustedCerts {
			pem, err := os.ReadFile(p)
			if err != nil {
				return nil, fmt.Errorf("reading trusted certificate %s: %w", p, err)
			}
			if !pool.AppendCertsFromPEM(pem) {
				return nil, fmt.Errorf("no certificates found in %s", p)
			}
		}
	}
	tr := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		TLSClientConfig:     &tls.Config{RootCAs: pool, InsecureSkipVerify: noSSLValidation},
		MaxIdleConnsPerHost: 16,
	}
	return &http.Client{Transport: tr, Timeout: timeout}, nil
}
