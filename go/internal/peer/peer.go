// Package peer is the Amphora -> Amphora inter-VCP client (POST /inter-vcp/open).
package peer

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Samrat7745/amphora/go/internal/httpx"
	"github.com/Samrat7745/amphora/go/internal/model"
)

// Client posts exchange objects to every partner.
type Client struct {
	partners []string
	http     *http.Client
}

// New creates the client; every partner must be a URI with a host.
func New(partners []string, noSSLValidation bool, trustedCerts []string, timeout time.Duration) (*Client, error) {
	var bases []string
	for _, p := range partners {
		u, err := url.Parse(p)
		if err != nil || u.Host == "" {
			return nil, fmt.Errorf("Invalid service address %q", p)
		}
		bases = append(bases, strings.TrimSuffix(p, "/"))
	}
	hc, err := httpx.NewClient(noSSLValidation, trustedCerts, timeout)
	if err != nil {
		return nil, err
	}
	return &Client{partners: bases, http: hc}, nil
}

// Open sends e to all partners in parallel. Any failure is returned.
func (c *Client) Open(ctx context.Context, e model.ExchangeObject) error {
	body := []byte(e.JSON())
	errs := make([]error, len(c.partners))
	var wg sync.WaitGroup
	for i, p := range c.partners {
		wg.Add(1)
		go func(i int, p string) {
			defer wg.Done()
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, p+"/inter-vcp/open", bytes.NewReader(body))
			if err != nil {
				errs[i] = err
				return
			}
			req.Header.Set("Accept", "application/json")
			req.Header.Set("Content-type", "application/json")
			resp, err := c.http.Do(req)
			if err != nil {
				errs[i] = err
				return
			}
			defer resp.Body.Close()
			_, _ = io.Copy(io.Discard, resp.Body)
			if resp.StatusCode < 200 || resp.StatusCode > 299 {
				errs[i] = fmt.Errorf("Request for endpoint %q has failed with status %d", p+"/inter-vcp/open", resp.StatusCode)
			}
		}(i, p)
	}
	wg.Wait()
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}
