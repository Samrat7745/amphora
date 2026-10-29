// Package castor is the client for Castor's GET /intra-vcp/tuples.
package castor

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/carbynestack/amphora-go/internal/httpx"
)

// Tuple types used by Amphora.
const (
	InputMaskGfp   = "INPUT_MASK_GFP"
	MultTripleGfp  = "MULTIPLICATION_TRIPLE_GFP"
	arityInputMask = 1
	arityTriple    = 3
)

// Share is one share of a tuple (value and mac as raw bytes).
type Share struct{ Value, Mac []byte }

// Tuple is the list of shares of one tuple.
type Tuple []Share

// Client downloads tuple shares.
type Client struct {
	base string
	http *http.Client
}

// New creates a client for the Castor service URI (must include the scheme).
func New(serviceURI string, noSSLValidation bool, trustedCerts []string) (*Client, error) {
	u, err := url.Parse(serviceURI)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("Invalid service address %q", serviceURI)
	}
	hc, err := httpx.NewClient(noSSLValidation, trustedCerts, 5*time.Minute)
	if err != nil {
		return nil, err
	}
	return &Client{base: strings.TrimSuffix(serviceURI, "/"), http: hc}, nil
}

// Error mimics CastorClientException.
type Error struct{ Msg string }

func (e *Error) Error() string { return e.Msg }

func (c *Client) fail(msg string) error {
	return &Error{fmt.Sprintf("Failed downloading tuples from service %s: %s", c.base, msg)}
}

type tupleListJSON struct {
	Tuples []struct {
		Shares []struct {
			Value *string `json:"value"`
			Mac   *string `json:"mac"`
		} `json:"shares"`
	} `json:"tuples"`
}

// Download fetches count tuples of the type reserved under requestID.
func (c *Client) Download(ctx context.Context, requestID, tupleType string, count int64) ([]Tuple, error) {
	u := fmt.Sprintf("%s/intra-vcp/tuples?count=%d&reservationId=%s&tupletype=%s", c.base, count, requestID, tupleType)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, c.fail("GET request failed.")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, c.fail("GET request failed.")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, c.fail("GET request failed.")
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, c.fail(fmt.Sprintf("Request has failed with status <%d>: %s", resp.StatusCode, body))
	}
	var tl tupleListJSON
	if err := json.Unmarshal(body, &tl); err != nil {
		return nil, c.fail("GET request failed.")
	}
	out := make([]Tuple, 0, len(tl.Tuples))
	for _, t := range tl.Tuples {
		tup := make(Tuple, 0, len(t.Shares))
		for _, s := range t.Shares {
			if s.Value == nil || s.Mac == nil {
				return nil, c.fail("GET request failed.")
			}
			v, err1 := base64.StdEncoding.DecodeString(*s.Value)
			m, err2 := base64.StdEncoding.DecodeString(*s.Mac)
			if err1 != nil || err2 != nil || len(v) != len(m) {
				return nil, c.fail("GET request failed.")
			}
			tup = append(tup, Share{v, m})
		}
		out = append(out, tup)
	}
	return out, nil
}
