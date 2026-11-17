// Package httpx builds HTTP clients honouring NO_SSL_VALIDATION / TRUSTED_CERTIFICATES.
package httpx

import (
	"net/http"
	"time"

	httpclient "github.com/Samrat7745/java-http-client"
)

// NewClient returns a client built with the Carbyne Stack HTTP client library; trusted certs are
// added to the system roots (ignored when noSSLValidation is set, like the Java Castor client).
func NewClient(noSSLValidation bool, trustedCerts []string, timeout time.Duration) (*http.Client, error) {
	c, err := httpclient.New[string](
		httpclient.WithoutSSLValidation(noSSLValidation),
		httpclient.WithTrustedCertificates(trustedCerts...),
		httpclient.WithTimeout(timeout),
		httpclient.WithMaxIdleConnsPerHost(16),
	)
	if err != nil {
		return nil, err
	}
	return c.HTTPClient(), nil
}
