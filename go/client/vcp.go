package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"strings"

	"github.com/google/uuid"
)

// IntraVcpClient is the service-to-service API of one Amphora (used e.g. by Ephemeral): no bearer token is sent.
type IntraVcpClient interface {
	UploadSecretShare(ctx context.Context, s SecretShare) (uuid.UUID, error)
	GetSecretShare(ctx context.Context, id uuid.UUID) (*SecretShare, error)
}

// InterVcpClient broadcasts interim multiplication values to the partner Amphoras.
type InterVcpClient interface {
	Open(ctx context.Context, o MultiplicationExchangeObject) error
}

type intra struct {
	*base
}

// NewIntraVcpClient builds a client for the first address in o.ServiceAddresses.
func NewIntraVcpClient(o Options) (IntraVcpClient, error) {
	if len(o.ServiceAddresses) == 0 {
		return nil, errors.New("Service URI must not be null.")
	}
	b, err := newBase(o)
	if err != nil {
		return nil, err
	}
	return &intra{b}, nil
}

type shareJSON struct {
	SecretID string `json:"secretId"`
	Tags     []Tag  `json:"tags"`
	Data     []byte `json:"data"`
}

func (c *intra) UploadSecretShare(ctx context.Context, s SecretShare) (uuid.UUID, error) {
	tags := s.Tags
	if tags == nil {
		tags = []Tag{}
	}
	r := c.do(ctx, "POST", c.addrs[0], "/intra-vcp/secret-shares", nil, shareJSON{s.SecretID.String(), tags, s.Data}, false)
	if r.err != nil {
		return uuid.Nil, &ClientError{"Creating SecretShare failed", r.err}
	}
	return s.SecretID, nil
}

func (c *intra) GetSecretShare(ctx context.Context, id uuid.UUID) (*SecretShare, error) {
	r := c.do(ctx, "GET", c.addrs[0], "/intra-vcp/secret-shares/"+id.String(), nil, nil, false)
	if r.err != nil {
		return nil, &ClientError{fmt.Sprintf("Fetching secret #%s failed", id), r.err}
	}
	var sj shareJSON
	if err := json.Unmarshal(r.body, &sj); err != nil {
		return nil, &ClientError{fmt.Sprintf("Fetching secret #%s failed", id), err}
	}
	sid, err := uuid.Parse(sj.SecretID)
	if err != nil {
		return nil, &ClientError{Msg: "invalid secret id in response", Cause: err}
	}
	return &SecretShare{SecretID: sid, Tags: sj.Tags, Data: sj.Data}, nil
}

type inter struct{ *base }

// NewInterVcpClient builds a client that broadcasts to every address in o.ServiceAddresses.
func NewInterVcpClient(o Options) (InterVcpClient, error) {
	if len(o.ServiceAddresses) == 0 {
		return nil, errors.New("At least one amphora service URI needs to be defined.")
	}
	b, err := newBase(o)
	if err != nil {
		return nil, err
	}
	return &inter{b}, nil
}

func bigNumber(v *big.Int) json.RawMessage { return json.RawMessage(v.String()) }

func (c *inter) Open(ctx context.Context, o MultiplicationExchangeObject) error {
	if o.OperationID == uuid.Nil {
		return errors.New("OperationId must not be null")
	}
	pairs := make([]map[string]json.RawMessage, len(o.InterimValues))
	for i, p := range o.InterimValues {
		pairs[i] = map[string]json.RawMessage{"a": bigNumber(p.A), "b": bigNumber(p.B)}
	}
	body := map[string]any{"operationId": o.OperationID.String(), "playerId": o.PlayerID, "interimValues": pairs}
	res, _ := c.all(ctx, failAtLeastOne, "POST", "/inter-vcp/open", url.Values{}, body)
	var lines []string
	for i, r := range res {
		if r.err != nil {
			lines = append(lines, fmt.Sprintf("Request for endpoint \"%s\" has failed: %s", c.addrs[i], r.err))
		}
	}
	if len(lines) > 0 {
		return &ClientError{AtLeastOneFailedPrefix + strings.Join(lines, "\n\t"), nil}
	}
	return nil
}
