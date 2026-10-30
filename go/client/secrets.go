package client

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"net/url"
	"strconv"
	"strings"

	"github.com/carbynestack/amphora-go/spdz"
	"github.com/google/uuid"
)

// Client is the user-facing Amphora client (all parties).
type Client interface {
	CreateSecret(ctx context.Context, s Secret) (uuid.UUID, error)
	GetSecret(ctx context.Context, id uuid.UUID) (*Secret, error)
	DeleteSecret(ctx context.Context, id uuid.UUID) error
	// GetSecrets lists secret metadata (all of it, no paging); filters and sort are optional.
	GetSecrets(ctx context.Context, filters []TagFilter, sort *Sort) ([]Metadata, error)
	// GetSecretsPage returns one page of secret metadata.
	GetSecretsPage(ctx context.Context, filters []TagFilter, page PageRequest) (*MetadataPage, error)
	GetTags(ctx context.Context, id uuid.UUID) ([]Tag, error)
	CreateTag(ctx context.Context, id uuid.UUID, t Tag) error
	OverwriteTags(ctx context.Context, id uuid.UUID, tags []Tag) error
	GetTag(ctx context.Context, id uuid.UUID, key string) (*Tag, error)
	UpdateTag(ctx context.Context, id uuid.UUID, t Tag) error
	DeleteTag(ctx context.Context, id uuid.UUID, key string) error
}

type amphora struct{ *base }

// New creates the client for the parties in o.ServiceAddresses.
func New(o Options) (Client, error) {
	if err := checkFieldParams(o); err != nil {
		return nil, err
	}
	b, err := newBase(o)
	if err != nil {
		return nil, err
	}
	return &amphora{b}, nil
}

type odo struct {
	SecretShares []byte `json:"secretShares"`
	RShares      []byte `json:"rShares"`
	VShares      []byte `json:"vShares"`
	WShares      []byte `json:"wShares"`
	UShares      []byte `json:"uShares"`
}

type verifiableShare struct {
	odo
	SecretID string `json:"secretId"`
	Tags     []Tag  `json:"tags"`
	Data     []byte `json:"data"`
}

func (v *verifiableShare) UnmarshalJSON(b []byte) error {
	if err := json.Unmarshal(b, &v.odo); err != nil {
		return err
	}
	var rest struct {
		SecretID string `json:"secretId"`
		Tags     []Tag  `json:"tags"`
		Data     []byte `json:"data"`
	}
	if err := json.Unmarshal(b, &rest); err != nil {
		return err
	}
	v.SecretID, v.Tags, v.Data = rest.SecretID, rest.Tags, rest.Data
	return nil
}

// recombine sums the party shares word by word (mod prime).
func recombine(sp *spdz.Utils, shares [][]byte) ([]*big.Int, error) {
	if len(shares) == 0 {
		return nil, nil
	}
	n := len(shares[0]) / spdz.WordWidth
	out := make([]*big.Int, n)
	for i := 0; i < n; i++ {
		sum := new(big.Int)
		for _, s := range shares {
			if len(s) != len(shares[0]) {
				return nil, &IntegrityError{"output delivery object shares differ in length"}
			}
			v, err := sp.FromGfp(s[i*spdz.WordWidth : (i+1)*spdz.WordWidth])
			if err != nil {
				return nil, err
			}
			sum.Add(sum, v)
		}
		out[i] = sum.Mod(sum, sp.Prime)
	}
	return out, nil
}

// verify recombines the five share arrays and checks w = s*r and u = v*r (Java verifyOutputDeliveryObjects).
func verify(sp *spdz.Utils, odos []odo) ([]*big.Int, error) {
	pick := func(f func(o odo) []byte) ([]*big.Int, error) {
		in := make([][]byte, len(odos))
		for i, o := range odos {
			in[i] = f(o)
		}
		return recombine(sp, in)
	}
	secrets, err := pick(func(o odo) []byte { return o.SecretShares })
	if err != nil {
		return nil, err
	}
	rs, err1 := pick(func(o odo) []byte { return o.RShares })
	us, err2 := pick(func(o odo) []byte { return o.UShares })
	vs, err3 := pick(func(o odo) []byte { return o.VShares })
	ws, err4 := pick(func(o odo) []byte { return o.WShares })
	for _, e := range []error{err1, err2, err3, err4} {
		if e != nil {
			return nil, e
		}
	}
	if len(rs) != len(secrets) || len(us) != len(secrets) || len(vs) != len(secrets) || len(ws) != len(secrets) {
		return nil, &IntegrityError{"output delivery object arrays differ in length"}
	}
	for i := range secrets {
		w := new(big.Int).Mod(new(big.Int).Mul(secrets[i], rs[i]), sp.Prime)
		u := new(big.Int).Mod(new(big.Int).Mul(vs[i], rs[i]), sp.Prime)
		if w.Cmp(ws[i]) != 0 || u.Cmp(us[i]) != 0 {
			return nil, &IntegrityError{fmt.Sprintf("Verification of secret has failed:\n"+
				"\t%s = %s * %s   &&   %s = %s * %s\n"+
				"\t%s = %s   &&   %s = %s",
				ws[i], secrets[i], rs[i], us[i], vs[i], rs[i], ws[i], w, us[i], u)}
		}
	}
	return secrets, nil
}

func (c *amphora) CreateSecret(ctx context.Context, s Secret) (uuid.UUID, error) {
	sp, err := c.spdz()
	if err != nil {
		return uuid.Nil, err
	}
	q := url.Values{"requestId": {s.SecretID.String()}, "count": {strconv.Itoa(len(s.Data))}}
	res, err := c.all(ctx, failResponses, "GET", "/input-masks", q, nil)
	if err != nil {
		return uuid.Nil, err
	}
	odos := make([]odo, len(res))
	for i, r := range res {
		if err := json.Unmarshal(r.body, &odos[i]); err != nil {
			return uuid.Nil, &ClientError{"Failed to parse input masks", err}
		}
	}
	masks, err := verify(sp, odos)
	if err != nil {
		return uuid.Nil, err
	}
	if len(masks) != len(s.Data) {
		return uuid.Nil, &ClientError{Msg: fmt.Sprintf("received %d input masks for %d secret elements", len(masks), len(s.Data))}
	}
	type word struct {
		Value []byte `json:"value"`
	}
	data := make([]word, len(s.Data))
	for i, x := range s.Data {
		m := new(big.Int).Mod(new(big.Int).Sub(x, masks[i]), sp.Prime)
		b, err := sp.ToGfp(m)
		if err != nil {
			return uuid.Nil, err
		}
		data[i] = word{b}
	}
	tags := s.Tags
	if tags == nil {
		tags = []Tag{}
	}
	body := map[string]any{"secretId": s.SecretID.String(), "data": data, "tags": tags}
	if _, err := c.all(ctx, failCheck, "POST", "/masked-inputs", nil, body); err != nil {
		return uuid.Nil, err
	}
	return s.SecretID, nil
}

func (c *amphora) GetSecret(ctx context.Context, id uuid.UUID) (*Secret, error) {
	sp, err := c.spdz()
	if err != nil {
		return nil, err
	}
	rid := uuid.New()
	res, err := c.all(ctx, failResponses, "GET", "/secret-shares/"+id.String(), url.Values{"requestId": {rid.String()}}, nil)
	if err != nil {
		return nil, err
	}
	vs := make([]verifiableShare, len(res))
	odos := make([]odo, len(res))
	for i, r := range res {
		if err := json.Unmarshal(r.body, &vs[i]); err != nil {
			return nil, &ClientError{"Failed to parse secret share", err}
		}
		odos[i] = vs[i].odo
	}
	secrets, err := verify(sp, odos)
	if err != nil {
		return nil, err
	}
	return &Secret{SecretID: id, Tags: vs[0].Tags, Data: secrets}, nil
}

func (c *amphora) DeleteSecret(ctx context.Context, id uuid.UUID) error {
	_, err := c.all(ctx, failAtLeastOne, "DELETE", "/secret-shares/"+id.String(), nil, nil)
	return err
}

// filterParam encodes criteria like DefaultAmphoraClient.addFilters: key<op>value joined by ",", parts URL-encoded.
func filterParam(fs []TagFilter) string {
	parts := make([]string, 0, len(fs))
	for _, f := range fs {
		parts = append(parts, url.QueryEscape(f.Key)+f.Operator+url.QueryEscape(f.Value))
	}
	return strings.Join(parts, ",")
}

func (c *amphora) page(ctx context.Context, q url.Values) (*MetadataPage, error) {
	r := c.do(ctx, "GET", c.addrs[0], "/secret-shares", q, nil, true)
	if r.err != nil {
		return nil, &ClientError{FetchingSecretMetadataFails, r.err}
	}
	var p MetadataPage
	if err := json.Unmarshal(r.body, &p); err != nil {
		return nil, &ClientError{FetchingSecretMetadataFails, err}
	}
	return &p, nil
}

func sortParams(q url.Values, s *Sort) {
	if s == nil {
		return
	}
	q.Set("sort", s.Property)
	if s.Desc {
		q.Set("dir", "DESC")
	} else {
		q.Set("dir", "ASC")
	}
}

func (c *amphora) GetSecrets(ctx context.Context, filters []TagFilter, sort *Sort) ([]Metadata, error) {
	q := url.Values{}
	if f := filterParam(filters); f != "" {
		q.Set("filter", f)
	}
	sortParams(q, sort)
	p, err := c.page(ctx, q)
	if err != nil {
		return nil, err
	}
	return p.Content, nil
}

func (c *amphora) GetSecretsPage(ctx context.Context, filters []TagFilter, page PageRequest) (*MetadataPage, error) {
	q := url.Values{}
	if f := filterParam(filters); f != "" {
		q.Set("filter", f)
	}
	q.Set("pageNumber", strconv.Itoa(page.Page))
	q.Set("pageSize", strconv.Itoa(page.Size))
	sortParams(q, page.Sort)
	return c.page(ctx, q)
}

func (c *amphora) randomAddr() string {
	n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(c.addrs))))
	return c.addrs[n.Int64()]
}

func (c *amphora) GetTags(ctx context.Context, id uuid.UUID) ([]Tag, error) {
	r := c.do(ctx, "GET", c.randomAddr(), "/secret-shares/"+id.String()+"/tags", nil, nil, true)
	if r.err != nil {
		return nil, &ClientError{fmt.Sprintf(FetchingTagsFailed, id), r.err}
	}
	var tags []Tag
	if err := json.Unmarshal(r.body, &tags); err != nil {
		return nil, &ClientError{fmt.Sprintf(FetchingTagsFailed, id), err}
	}
	return tags, nil
}

func (c *amphora) CreateTag(ctx context.Context, id uuid.UUID, t Tag) error {
	_, err := c.all(ctx, failCheck, "POST", "/secret-shares/"+id.String()+"/tags", nil, t)
	return err
}

func (c *amphora) OverwriteTags(ctx context.Context, id uuid.UUID, tags []Tag) error {
	if tags == nil {
		tags = []Tag{}
	}
	_, err := c.all(ctx, failAtLeastOne, "PUT", "/secret-shares/"+id.String()+"/tags", nil, tags)
	return err
}

func tagPath(id uuid.UUID, key string) string {
	return "/secret-shares/" + id.String() + "/tags/" + url.PathEscape(key)
}

func (c *amphora) GetTag(ctx context.Context, id uuid.UUID, key string) (*Tag, error) {
	r := c.do(ctx, "GET", c.randomAddr(), tagPath(id, key), nil, nil, true)
	if r.err != nil {
		return nil, &ClientError{fmt.Sprintf(FetchingTagWithKeyFailed, key, id), r.err}
	}
	var t Tag
	if err := json.Unmarshal(r.body, &t); err != nil {
		return nil, &ClientError{fmt.Sprintf(FetchingTagWithKeyFailed, key, id), err}
	}
	return &t, nil
}

func (c *amphora) UpdateTag(ctx context.Context, id uuid.UUID, t Tag) error {
	_, err := c.all(ctx, failAtLeastOne, "PUT", tagPath(id, t.Key), nil, t)
	return err
}

func (c *amphora) DeleteTag(ctx context.Context, id uuid.UUID, key string) error {
	_, err := c.all(ctx, failAtLeastOne, "DELETE", tagPath(id, key), nil, nil)
	return err
}
