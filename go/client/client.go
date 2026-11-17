package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Samrat7745/amphora/go/spdz"
	httpclient "github.com/Samrat7745/java-http-client"
)

// Messages mirror DefaultAmphoraClient.
const (
	FetchingTagWithKeyFailed    = "Fetching tag with key \"%s\" for secret #%s failed"
	FetchingTagsFailed          = "Fetching tags for secret #%s failed"
	FetchingSecretMetadataFails = "Fetching secret metadata failed"
	RequestForEndpointFailed    = "Request for endpoint \"%s\" failed: %s"

	// CreateFailedPrefix starts the error of createSecret/createTag when a service rejects the request.
	CreateFailedPrefix = "Secret could not be created due to http errors returned by the following providers: "
	// AtLeastOneFailedPrefix starts the error of update/delete requests with failing services.
	AtLeastOneFailedPrefix = "At least one request has failed:\n\t"
	// ResponsesFailedPrefix starts the error of download requests with failing services.
	ResponsesFailedPrefix = "Error(s) occurred while processing responses:\n\t"
)

// failMode selects how failures of a request to all services are reported (Java: checkSuccess and
// the two unwrap variants of DefaultAmphoraClient).
type failMode int

const (
	failCheck      failMode = iota // checkSuccess: one line per failed endpoint
	failAtLeastOne                 // unwrap(List<Try>)
	failResponses                  // unwrap(Map<URI, Try>)
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
		return nil, errors.New("At least one amphora service uri has to be provided.")
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
	to := o.Timeout
	if to == 0 {
		to = 60 * time.Second
	}
	// The HTTP client library provides the trust management (system roots plus
	// TrustedCertificates) and the redirect policy of the Java client.
	hc, err := httpclient.New[string](
		httpclient.WithoutSSLValidation(o.NoSSLValidation),
		httpclient.WithTrustedCertificates(o.TrustedCertificates...),
		httpclient.WithTimeout(to),
		httpclient.WithMaxIdleConnsPerHost(8),
	)
	if err != nil {
		if cause := errors.Unwrap(err); cause != nil {
			err = cause
		}
		return nil, &ClientError{"Failed to create AmphoraCommunicationClient.", err}
	}
	return &base{opt: o, addrs: addrs, http: hc.HTTPClient()}, nil
}

// queryOrder is the order in which the Java client appends its query parameters; others follow sorted.
var queryOrder = []string{"requestId", "count", "filter", "pageNumber", "pageSize", "sort", "dir"}

func encodeQuery(q url.Values) string {
	var parts []string
	seen := map[string]bool{}
	for _, k := range queryOrder {
		if v, ok := q[k]; ok {
			seen[k] = true
			for _, x := range v {
				parts = append(parts, url.QueryEscape(k)+"="+url.QueryEscape(x))
			}
		}
	}
	rest := url.Values{}
	for k, v := range q {
		if !seen[k] {
			rest[k] = v
		}
	}
	if len(rest) > 0 {
		parts = append(parts, rest.Encode())
	}
	return strings.Join(parts, "&")
}

type result struct {
	status int
	body   []byte
	err    error
}

func (b *base) do(ctx context.Context, method, addr, path string, query url.Values, body any, auth bool) result {
	u := addr + path
	if len(query) > 0 {
		u += "?" + encodeQuery(query)
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
		token := httpclient.BearerToken(b.opt.BearerTokenProvider(addr))
		req.Header.Set(token.Name, token.Value)
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
func (b *base) all(ctx context.Context, mode failMode, method, path string, query url.Values, body any) ([]result, error) {
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
	return res, joinErrors(b.addrs, res, mode)
}

func joinErrors(addrs []string, res []result, mode failMode) error {
	var lines []string
	var first error
	for i, r := range res {
		if r.err == nil {
			continue
		}
		if first == nil {
			first = r.err
		}
		switch mode {
		case failCheck:
			lines = append(lines, fmt.Sprintf(RequestForEndpointFailed, addrs[i], r.err.Error()))
		default:
			lines = append(lines, r.err.Error())
		}
	}
	if len(lines) == 0 {
		return nil
	}
	switch mode {
	case failCheck:
		return &ClientError{CreateFailedPrefix + "\n\t" + strings.Join(lines, "\n\t"), first}
	case failResponses:
		return &ClientError{ResponsesFailedPrefix + strings.Join(lines, "\n\t"), first}
	}
	return &ClientError{AtLeastOneFailedPrefix + strings.Join(lines, "\n\t"), first}
}

// checkFieldParams is the mandatory configuration of the user facing client (builder.build()).
func checkFieldParams(o Options) error {
	if o.Prime == nil {
		return errors.New("Prime must not be null")
	}
	if o.R == nil {
		return errors.New("Auxiliary modulus R must not be null")
	}
	if o.RInv == nil {
		return errors.New("Multiplicative inverse for the auxiliary modulus R must not be null")
	}
	return nil
}

func (b *base) spdz() (*spdz.Utils, error) {
	if err := checkFieldParams(b.opt); err != nil {
		return nil, &ClientError{Msg: err.Error()}
	}
	return spdz.New(b.opt.Prime, b.opt.R, b.opt.RInv), nil
}
