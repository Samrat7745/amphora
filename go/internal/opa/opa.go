// Package opa implements the OPA client/service (policy package override, action paths) and the
// JWT claim extraction of the Java service.
package opa

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/carbynestack/amphora-go/internal/model"
)

// Action names (path suffixes).
const (
	Read      = "read"
	Use       = "use"
	Delete    = "delete"
	TagCreate = "tag/create"
	TagRead   = "tag/read"
	TagUpdate = "tag/update"
	TagDelete = "tag/delete"

	PolicyPackageTagKey = "accessPolicy"
	OwnerTagKey         = "owner"
)

// Client evaluates policies.
type Client struct {
	endpoint       *url.URL
	defaultPackage string
	http           *http.Client
	log            *slog.Logger
}

// New creates a client; the default HTTP client uses the system trust store (like the JVM).
func New(endpoint, defaultPackage string, log *slog.Logger) (*Client, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}
	if log == nil {
		log = slog.Default()
	}
	return &Client{endpoint: u, defaultPackage: defaultPackage, http: &http.Client{Timeout: 60 * time.Second}, log: log}, nil
}

// Allowed evaluates action for subject over tags. Subject and action must not be empty
// (CsOpaException -> 500). Any transport/protocol error counts as "denied".
func (c *Client) Allowed(ctx context.Context, subject, action string, tags []model.Tag) (bool, error) {
	if subject == "" {
		return false, model.Internal("Subject is required to evaluate the policy")
	}
	if action == "" {
		return false, model.Internal("Action is required to evaluate the policy")
	}
	pkg := c.defaultPackage
	for _, t := range tags {
		if t.Key == PolicyPackageTagKey {
			pkg = t.Value
			break
		}
	}
	// URI.resolve("/v1/data/..."): an absolute path replaces any path of the endpoint.
	target := *c.endpoint
	target.Path = "/v1/data/" + strings.ReplaceAll(pkg, ".", "/") + "/" + action
	target.RawPath = ""
	target.RawQuery = ""
	target.Fragment = ""
	body := `{"input":{"subject":` + model.Quote(subject) + `,"tags":` + model.TagsJSON(nonNil(tags)) + `}}`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), bytes.NewReader([]byte(body)))
	if err != nil {
		c.log.Error("Error occurred while evaluating OPA policy package", "err", err)
		return false, nil
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		c.log.Error("Error occurred while evaluating OPA policy package", "err", err)
		return false, nil
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		c.log.Error("Error occurred while evaluating OPA policy package", "status", resp.StatusCode)
		return false, nil
	}
	var res struct {
		Result *bool `json:"result"`
	}
	if err := json.Unmarshal(data, &res); err != nil {
		c.log.Error("Error occurred while evaluating OPA policy package", "err", err)
		return false, nil
	}
	return res.Result != nil && *res.Result, nil
}

func nonNil(t []model.Tag) []model.Tag {
	if t == nil {
		return []model.Tag{}
	}
	return t
}
