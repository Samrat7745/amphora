package client

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/carbynestack/amphora-go/spdz"
)

// Messages mirror DefaultAmphoraClient.
const (
	FetchingTagWithKeyFailed    = "Fetching tag with key %s for secret %s failed"
	FetchingTagsFailed          = "Fetching tags for secret %s failed"
	FetchingSecretMetadataFails = "Fetching secret metadata failed"
	RequestForEndpointFailed    = "Request for endpoint %s failed: %s"
)

// ClientError is Java's AmphoraClientException.
type ClientError struct {
	Msg   string
	Cause error
}

func (e *ClientError) Error() string {
	if e.Cause == nil || strings.Contains(e.Msg, e.Cause.Error()) {
		return e.Msg
	}
	return e.Msg + ": " + e.Cause.Error()
}
func (e *ClientError) Unwrap() error { return e.Cause }

// IntegrityError is Java's IntegrityVerificationException.
type IntegrityError struct{ Msg string }

func (e *IntegrityError) Error() string { return e.Msg }

// BearerTokenProvider returns the token for a service address.
type BearerTokenProvider func(serviceAddress string) string

// Options configure a client.
type Options struct {
	// ServiceAddresses has one Amphora base URI per party (index = player id).
	ServiceAddresses []string
	// Prime, R and RInv are the SPDZ field parameters (needed for secret sharing).
	Prime, R, RInv      *big.Int
	NoSSLValidation     bool
	TrustedCertificates []string
	BearerTokenProvider BearerTokenProvider
	Timeout             time.Duration
}

type base struct {
	opt   Options
	addrs []string
	http  *http.Client
}

func newBase(o Options) (*base, error) {
	if len(o.ServiceAddresses) == 0 {
		return nil, errors.New("Declared service URIs must not be empty")
	}
	var addrs []string
	for _, a := range o.ServiceAddresses {
		u, err := url.Parse(a)
		if strings.TrimSpace(a) == "" || err != nil || u.Hostname() == "" {
			return nil, fmt.Errorf("Invalid service address: %q", a)
		}
		if u.Scheme != "https" {
			u.Scheme = "http"
		}
		addrs = append(addrs, strings.TrimSuffix(u.String(), "/"))
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	for _, p := range o.TrustedCertificates {
		pem, err := os.ReadFile(p)
		if err != nil {
			return nil, &ClientError{"Failed to create client", err}
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, &ClientError{"Failed to create client", fmt.Errorf("no certificates in %s", p)}
		}
	}
	to := o.Timeout
	if to == 0 {
		to = 60 * time.Second
	}
	return &base{opt: o, addrs: addrs, http: &http.Client{Timeout: to, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool, InsecureSkipVerify: o.NoSSLValidation}, MaxIdleConnsPerHost: 8}}}, nil
}

type result struct {
	status int
	body   []byte
	err    error
}

func (b *base) do(ctx context.Context, method, addr, path string, query url.Values, body any, auth bool) result {
	u := addr + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return result{err: err}
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return result{err: err}
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if auth && b.opt.BearerTokenProvider != nil {
		req.Header.Set("Authorization", "Bearer "+b.opt.BearerTokenProvider(addr))
	}
	resp, err := b.http.Do(req)
	if err != nil {
		return result{err: err}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	r := result{status: resp.StatusCode, body: raw}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		r.err = fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return r
}

// all runs the request against every service in parallel and returns the results in service order.
func (b *base) all(ctx context.Context, method, path string, query url.Values, body any) ([]result, error) {
	res := make([]result, len(b.addrs))
	var wg sync.WaitGroup
	for i, a := range b.addrs {
		wg.Add(1)
		go func(i int, a string) {
			defer wg.Done()
			res[i] = b.do(ctx, method, a, path, query, body, true)
		}(i, a)
	}
	wg.Wait()
	return res, joinErrors(b.addrs, res)
}

func joinErrors(addrs []string, res []result) error {
	var msgs []string
	var first error
	for i, r := range res {
		if r.err != nil {
			msgs = append(msgs, fmt.Sprintf(RequestForEndpointFailed, addrs[i], r.err.Error()))
			if first == nil {
				first = r.err
			}
		}
	}
	if len(msgs) == 0 {
		return nil
	}
	return &ClientError{strings.Join(msgs, "\n\t"), first}
}

func (b *base) spdz() (*spdz.Utils, error) {
	if b.opt.Prime == nil || b.opt.R == nil || b.opt.RInv == nil {
		return nil, &ClientError{Msg: "Prime, R and RInv must be set to handle secret data"}
	}
	return spdz.New(b.opt.Prime, b.opt.R, b.opt.RInv), nil
}
