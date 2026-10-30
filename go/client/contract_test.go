package client

// Contract tests without a rig: the public client talks to the real in-process httpapi handlers of N parties
// (httptest). The parties' Backend is an in-memory implementation with the semantics of the storage service
// (tags, listing with filter/sort/paging, secret sharing with a trusted dealer for the multiplication), so the wire
// formats of client and server are exercised together on every build. The Postgres/Redis/MinIO backed behaviour
// is covered by the service and system tests.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/carbynestack/amphora-go/internal/httpapi"
	"github.com/carbynestack/amphora-go/internal/jwt"
	"github.com/carbynestack/amphora-go/internal/model"
	"github.com/carbynestack/amphora-go/internal/pgstore"
	"github.com/carbynestack/amphora-go/spdz"
	"github.com/google/uuid"
)

const contractUser = "afc0117f-c9cd-4d8c-acee-fa1433ca0fdd"

// cluster holds the shared dealer state of all parties.
type cluster struct {
	t       *testing.T
	sp      *spdz.Utils
	mu      sync.Mutex
	parties []*memParty
	odos    map[string][]model.OutputDeliveryObject // requestId -> per party
	corrupt bool                                    // add 1 to w when dealing (integrity failure)
}

type storedSecret struct {
	tags []model.Tag
	data []byte
}

type memParty struct {
	c       *cluster
	idx     int
	mu      sync.Mutex
	secrets map[string]*storedSecret
	masks   map[string]*big.Int // requestId -> this party's share of the first mask
	maskSh  map[string][]*big.Int
	interim []model.ExchangeObject
	failing bool
}

func newCluster(t *testing.T, n int) (*cluster, []*httptest.Server) {
	c := &cluster{t: t, sp: params(), odos: map[string][]model.OutputDeliveryObject{}}
	var servers []*httptest.Server
	for i := 0; i < n; i++ {
		p := &memParty{c: c, idx: i, secrets: map[string]*storedSecret{}, maskSh: map[string][]*big.Int{}}
		c.parties = append(c.parties, p)
		api := &httpapi.API{Svc: p, ODO: partyODO{p}, JWT: jwt.Reader{Field: "sub"}}
		h := api.Handler()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if p.failing {
				http.Error(w, "party is down", http.StatusServiceUnavailable)
				return
			}
			h.ServeHTTP(w, r)
		}))
		t.Cleanup(srv.Close)
		servers = append(servers, srv)
	}
	return c, servers
}

func token(string) string {
	return "h." + base64.StdEncoding.EncodeToString([]byte(`{"sub":"`+contractUser+`"}`)) + ".s"
}

func (c *cluster) options(servers []*httptest.Server) Options {
	var addrs []string
	for _, s := range servers {
		addrs = append(addrs, s.URL)
	}
	o := opts(addrs...)
	o.BearerTokenProvider = token
	return o
}

func (c *cluster) newClient(servers []*httptest.Server) Client {
	c.t.Helper()
	cl, err := New(c.options(servers))
	if err != nil {
		c.t.Fatal(err)
	}
	return cl
}

func (c *cluster) deal(secrets []*big.Int, n int) []model.OutputDeliveryObject {
	out := make([]model.OutputDeliveryObject, n)
	enc := func(x *big.Int) []byte {
		b, err := c.sp.ToGfp(x)
		if err != nil {
			c.t.Fatal(err)
		}
		return b
	}
	for _, s := range secrets {
		r, v := rnd(c.sp.Prime), rnd(c.sp.Prime)
		w := new(big.Int).Mod(new(big.Int).Mul(s, r), c.sp.Prime)
		u := new(big.Int).Mod(new(big.Int).Mul(v, r), c.sp.Prime)
		if c.corrupt {
			w.Add(w, big.NewInt(1))
		}
		parts := [][]*big.Int{split(c.sp.Prime, s, n), split(c.sp.Prime, r, n), split(c.sp.Prime, v, n), split(c.sp.Prime, w, n), split(c.sp.Prime, u, n)}
		for j := 0; j < n; j++ {
			out[j].SecretShares = append(out[j].SecretShares, enc(parts[0][j])...)
			out[j].RShares = append(out[j].RShares, enc(parts[1][j])...)
			out[j].VShares = append(out[j].VShares, enc(parts[2][j])...)
			out[j].WShares = append(out[j].WShares, enc(parts[3][j])...)
			out[j].UShares = append(out[j].UShares, enc(parts[4][j])...)
		}
	}
	return out
}

// ---- memParty implements httpapi.Backend ----

func notFound(id string) error { return model.NotFound(service_NoSecret, id) }

const service_NoSecret = "No secret with the given id #%s exists."

func (p *memParty) get(id string) (*storedSecret, error) {
	s, ok := p.secrets[id]
	if !ok {
		return nil, notFound(id)
	}
	return s, nil
}

func (p *memParty) InputMasks(_ context.Context, rid string, count int64) (model.OutputDeliveryObject, error) {
	c := p.c
	c.mu.Lock()
	defer c.mu.Unlock()
	odos, ok := c.odos[rid]
	if !ok {
		masks := make([]*big.Int, count)
		for i := range masks {
			masks[i] = rnd(c.sp.Prime)
		}
		odos = c.deal(masks, len(c.parties))
		c.odos[rid] = odos
		for j, q := range c.parties {
			q.mu.Lock()
			for i := range masks {
				v, _ := c.sp.FromGfp(odos[j].SecretShares[i*spdz.WordWidth : (i+1)*spdz.WordWidth])
				q.maskSh[rid] = append(q.maskSh[rid], v)
			}
			q.mu.Unlock()
		}
	}
	return odos[p.idx], nil
}

func (p *memParty) CreateSecret(_ context.Context, mi *model.MaskedInput, user string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.secrets[mi.SecretID]; ok {
		return "", model.AlreadyExists("A secret with the given id already exists.")
	}
	masks, ok := p.maskSh[mi.SecretID]
	if !ok {
		return "", model.Internal("No InputMaskList available in Cache for requestId %s", mi.SecretID)
	}
	var data []byte
	for i, d := range mi.Data {
		v := new(big.Int).Set(masks[i])
		if p.idx == 0 {
			m, err := p.c.sp.FromGfp(d)
			if err != nil {
				return "", model.BadRequest("%s", err.Error())
			}
			v.Add(v, m).Mod(v, p.c.sp.Prime)
		}
		b, _ := p.c.sp.ToGfp(v)
		data = append(data, b...)
		data = append(data, make([]byte, spdz.WordWidth)...) // mac
	}
	tags := append([]model.Tag{}, mi.Tags...)
	tags = append(tags, model.Tag{Key: "creationDate", Value: "1700000000000", ValueType: model.TypeString}, model.Tag{Key: "owner", Value: user, ValueType: model.TypeString})
	p.secrets[mi.SecretID] = &storedSecret{tags: tags, data: data}
	return mi.SecretID, nil
}

func (p *memParty) StoreSecretShare(_ context.Context, sh *model.SecretShare) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.secrets[sh.SecretID]; ok {
		return "", model.AlreadyExists("A secret with the given id already exists.")
	}
	p.secrets[sh.SecretID] = &storedSecret{tags: sh.Tags, data: sh.Data}
	return sh.SecretID, nil
}

func (p *memParty) UseSecretShare(_ context.Context, id, program string) (*model.SecretShare, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s, err := p.get(id)
	if err != nil {
		return nil, err
	}
	return &model.SecretShare{Metadata: model.Metadata{SecretID: id, Tags: s.tags}, Data: s.data}, nil
}

func (p *memParty) GetSecretShare(ctx context.Context, id, user string) (*model.SecretShare, error) {
	return p.UseSecretShare(ctx, id, user)
}

func (p *memParty) DeleteSecret(_ context.Context, id, _ string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, err := p.get(id); err != nil {
		return err
	}
	delete(p.secrets, id)
	return nil
}

func (p *memParty) RetrieveTags(_ context.Context, id, _ string) ([]model.Tag, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s, err := p.get(id)
	if err != nil {
		return nil, err
	}
	return append([]model.Tag{}, s.tags...), nil
}

func (p *memParty) idxOf(s *storedSecret, key string) int {
	for i, t := range s.tags {
		if t.Key == key {
			return i
		}
	}
	return -1
}

func reserved(k string) bool { return k == "creationDate" || k == "owner" }

func (p *memParty) StoreTag(_ context.Context, id string, t model.Tag, _ string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if reserved(t.Key) {
		return "", model.BadRequest("\"%s\" is a reserved key.", t.Key)
	}
	s, err := p.get(id)
	if err != nil {
		return "", err
	}
	if p.idxOf(s, t.Key) >= 0 {
		return "", model.AlreadyExists(fmt.Sprintf("A tag with key \"%s\" already exists for secret #%s", t.Key, id))
	}
	s.tags = append(s.tags, t)
	return t.Key, nil
}

func (p *memParty) ReplaceTags(_ context.Context, id string, tags []model.Tag, _ string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	s, err := p.get(id)
	if err != nil {
		return err
	}
	var keep []model.Tag
	for _, t := range s.tags {
		if reserved(t.Key) {
			keep = append(keep, t)
		}
	}
	for _, t := range tags {
		if !reserved(t.Key) {
			keep = append(keep, t)
		}
	}
	s.tags = keep
	return nil
}

func (p *memParty) RetrieveTag(_ context.Context, id, key, _ string) (model.Tag, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s, err := p.get(id)
	if err != nil {
		return model.Tag{}, err
	}
	i := p.idxOf(s, key)
	if i < 0 {
		return model.Tag{}, model.NotFound("No tag with key \"%s\" exists for secret with id #%s.", key, id)
	}
	return s.tags[i], nil
}

func (p *memParty) UpdateTag(_ context.Context, id string, t model.Tag, _ string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	s, err := p.get(id)
	if err != nil {
		return err
	}
	i := p.idxOf(s, t.Key)
	if i < 0 {
		return model.NotFound("No tag with key \"%s\" exists for secret with id #%s.", t.Key, id)
	}
	s.tags[i] = t
	return nil
}

func (p *memParty) DeleteTag(_ context.Context, id, key, _ string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	s, err := p.get(id)
	if err != nil {
		return err
	}
	i := p.idxOf(s, key)
	if i < 0 {
		return model.NotFound("No tag with key \"%s\" exists for secret with id #%s.", key, id)
	}
	s.tags = append(s.tags[:i], s.tags[i+1:]...)
	return nil
}

func (p *memParty) PutInterim(_ context.Context, e model.ExchangeObject) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.interim = append(p.interim, e)
	return nil
}

func tagVal(tags []model.Tag, k string) (model.Tag, bool) {
	for _, t := range tags {
		if t.Key == k {
			return t, true
		}
	}
	return model.Tag{}, false
}

func (p *memParty) List(_ context.Context, lp pgstore.ListParams) (model.MetadataPage, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var ids []string
	for id, s := range p.secrets {
		ok := true
		for _, f := range lp.Filters {
			t, has := tagVal(s.tags, f.Key)
			if !has {
				ok = false
				break
			}
			switch f.Op {
			case model.OpEquals:
				ok = ok && t.Value == f.Value
			default:
				a, e1 := strconv.ParseInt(t.Value, 10, 64)
				b, e2 := strconv.ParseInt(f.Value, 10, 64)
				if e1 != nil || e2 != nil {
					ok = false
				} else if f.Op == model.OpGreaterThan {
					ok = ok && a > b
				} else {
					ok = ok && a < b
				}
			}
		}
		if ok {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	if lp.SortKey != "" {
		key := func(id string) (string, bool) { t, ok := tagVal(p.secrets[id].tags, lp.SortKey); return t.Value, ok }
		sort.SliceStable(ids, func(i, j int) bool {
			a, aok := key(ids[i])
			b, bok := key(ids[j])
			less := func(x, y string) bool {
				xn, e1 := strconv.ParseInt(x, 10, 64)
				yn, e2 := strconv.ParseInt(y, 10, 64)
				if e1 == nil && e2 == nil {
					return xn < yn
				}
				return x < y
			}
			if !aok || !bok {
				return !aok && bok == lp.SortDesc // NULLs last ascending, first descending
			}
			if lp.SortDesc {
				return less(b, a)
			}
			return less(a, b)
		})
	}
	total := int64(len(ids))
	size, number := int64(1<<31-1), int64(0)
	if lp.Paged {
		size, number = int64(lp.PageSize), int64(lp.Page)
	}
	lo := min(number*size, total)
	hi := min(lo+size, total)
	page := model.MetadataPage{Content: []model.Metadata{}, Number: int32(number), Size: int32(size), TotalElements: total, TotalPages: int32((total + size - 1) / size)}
	for _, id := range ids[lo:hi] {
		page.Content = append(page.Content, model.Metadata{SecretID: id, Tags: append([]model.Tag{}, p.secrets[id].tags...)})
	}
	return page, nil
}

// partyODO computes the output delivery object of party idx with the dealer (the real service multiplies with its peers).
type partyODO struct{ p *memParty }

func (o partyODO) ComputeFromSecretShareData(_ context.Context, data []byte, rid string) (model.OutputDeliveryObject, error) {
	c := o.p.c
	c.mu.Lock()
	defer c.mu.Unlock()
	if odos, ok := c.odos[rid]; ok {
		return odos[o.p.idx], nil
	}
	// locate the secret by this party's data and sum all parties' shares
	var id string
	for k, s := range o.p.secrets {
		if string(s.data) == string(data) {
			id = k
		}
	}
	n := len(data) / spdz.ShareWidth
	words := make([]*big.Int, n)
	for i := range words {
		words[i] = new(big.Int)
		for _, q := range c.parties {
			q.mu.Lock()
			s := q.secrets[id]
			q.mu.Unlock()
			if s == nil {
				return model.OutputDeliveryObject{}, model.Internal("peer %d does not hold %s", q.idx, id)
			}
			v, _ := c.sp.FromGfp(s.data[i*spdz.ShareWidth : i*spdz.ShareWidth+spdz.WordWidth])
			words[i].Add(words[i], v)
		}
		words[i].Mod(words[i], c.sp.Prime)
	}
	odos := c.deal(words, len(c.parties))
	// the dealt secret shares must be this party's real shares, so replace them with the stored ones
	for j, q := range c.parties {
		q.mu.Lock()
		var ss []byte
		for i := 0; i < n; i++ {
			ss = append(ss, q.secrets[id].data[i*spdz.ShareWidth:i*spdz.ShareWidth+spdz.WordWidth]...)
		}
		q.mu.Unlock()
		odos[j].SecretShares = ss
	}
	c.odos[rid] = odos
	return odos[o.p.idx], nil
}

// ---- the tests ----

func big64(vs ...int64) []*big.Int {
	out := make([]*big.Int, len(vs))
	for i, v := range vs {
		out[i] = big.NewInt(v)
	}
	return out
}

func TestContractSecretLifecycle(t *testing.T) {
	for _, n := range []int{1, 2, 3} {
		t.Run(fmt.Sprintf("%d parties", n), func(t *testing.T) {
			c, srv := newCluster(t, n)
			cl := c.newClient(srv)
			ctx := context.Background()
			prime := c.sp.Prime
			secret := NewSecret([]Tag{{Key: "k", Value: "v"}, {Key: "n", Value: "5", ValueType: TagLong}},
				[]*big.Int{big.NewInt(0), big.NewInt(1), big.NewInt(424242), new(big.Int).Sub(prime, big.NewInt(1)), rnd(prime)})
			id, err := cl.CreateSecret(ctx, secret)
			if err != nil || id != secret.SecretID {
				t.Fatal(id, err)
			}
			// every party holds a share, none holds the plaintext
			for _, p := range c.parties {
				if _, ok := p.secrets[id.String()]; !ok {
					t.Fatalf("party %d has no share", p.idx)
				}
			}
			got, err := cl.GetSecret(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if got.SecretID != id || len(got.Data) != len(secret.Data) {
				t.Fatalf("%+v", got)
			}
			for i := range secret.Data {
				if got.Data[i].Cmp(secret.Data[i]) != 0 {
					t.Fatalf("element %d: got %v want %v", i, got.Data[i], secret.Data[i])
				}
			}
			tags := map[string]Tag{}
			for _, tg := range got.Tags {
				tags[tg.Key] = tg
			}
			if tags["k"].Value != "v" || tags["k"].ValueType != TagString || tags["n"].ValueType != TagLong || tags["owner"].Value != contractUser || tags["creationDate"].Value == "" {
				t.Fatalf("%v", got.Tags)
			}
			// duplicate creation is rejected by the service (409) and surfaced by the client
			if _, err := cl.CreateSecret(ctx, secret); err == nil || !strings.Contains(err.Error(), "409") {
				t.Fatalf("%v", err)
			}
			if err := cl.DeleteSecret(ctx, id); err != nil {
				t.Fatal(err)
			}
			if _, err := cl.GetSecret(ctx, id); err == nil || !strings.Contains(err.Error(), "No secret with the given id #"+id.String()+" exists.") || !strings.Contains(err.Error(), "404") {
				t.Fatalf("%v", err)
			}
			if err := cl.DeleteSecret(ctx, id); err == nil || !strings.HasPrefix(err.Error(), AtLeastOneFailedPrefix) {
				t.Fatalf("%v", err)
			}
		})
	}
}

func TestContractIntegrityFailureAndPartyDown(t *testing.T) {
	c, srv := newCluster(t, 2)
	cl := c.newClient(srv)
	ctx := context.Background()
	c.corrupt = true
	_, err := cl.CreateSecret(ctx, NewSecret(nil, big64(1)))
	var ie *IntegrityError
	if !errors.As(err, &ie) {
		t.Fatalf("want IntegrityError, got %v", err)
	}
	c.corrupt = false
	c.parties[1].failing = true
	_, err = cl.CreateSecret(ctx, NewSecret(nil, big64(1)))
	var ce *ClientError
	if !errors.As(err, &ce) || !strings.HasPrefix(err.Error(), ResponsesFailedPrefix) || !strings.Contains(err.Error(), "503") {
		t.Fatalf("%v", err)
	}
}

func TestContractTags(t *testing.T) {
	c, srv := newCluster(t, 2)
	cl := c.newClient(srv)
	ctx := context.Background()
	s := NewSecret([]Tag{{Key: "a", Value: "1", ValueType: TagLong}}, big64(7))
	if _, err := cl.CreateSecret(ctx, s); err != nil {
		t.Fatal(err)
	}
	id := s.SecretID
	tg, err := cl.GetTag(ctx, id, "a")
	if err != nil || *tg != (Tag{Key: "a", Value: "1", ValueType: TagLong}) {
		t.Fatal(tg, err)
	}
	if err := cl.CreateTag(ctx, id, Tag{Key: "b", Value: "x y/z"}); err != nil {
		t.Fatal(err)
	}
	if err := cl.CreateTag(ctx, id, Tag{Key: "b", Value: "again"}); err == nil || !strings.HasPrefix(err.Error(), CreateFailedPrefix) || !strings.Contains(err.Error(), "409") {
		t.Fatalf("%v", err)
	}
	// keys with characters that need path escaping
	if err := cl.CreateTag(ctx, id, Tag{Key: "k.with-dash_and.dots", Value: "v"}); err != nil {
		t.Fatal(err)
	}
	if got, err := cl.GetTag(ctx, id, "k.with-dash_and.dots"); err != nil || got.Value != "v" {
		t.Fatal(got, err)
	}
	if err := cl.UpdateTag(ctx, id, Tag{Key: "b", Value: "7", ValueType: TagLong}); err != nil {
		t.Fatal(err)
	}
	if got, _ := cl.GetTag(ctx, id, "b"); got.Value != "7" || got.ValueType != TagLong {
		t.Fatal(got)
	}
	// every party was updated, not only the one read from
	for _, p := range c.parties {
		if i := p.idxOf(p.secrets[id.String()], "b"); i < 0 || p.secrets[id.String()].tags[i].Value != "7" {
			t.Fatalf("party %d not updated", p.idx)
		}
	}
	if err := cl.UpdateTag(ctx, id, Tag{Key: "missing", Value: "1"}); err == nil || !strings.HasPrefix(err.Error(), AtLeastOneFailedPrefix) || !strings.Contains(err.Error(), "404") {
		t.Fatalf("%v", err)
	}
	if err := cl.CreateTag(ctx, id, Tag{Key: "owner", Value: "me"}); err == nil || !strings.Contains(err.Error(), `\"owner\" is a reserved key.`) {
		t.Fatalf("%v", err)
	}
	if err := cl.OverwriteTags(ctx, id, []Tag{{Key: "only", Value: "one"}}); err != nil {
		t.Fatal(err)
	}
	all, err := cl.GetTags(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	keys := []string{}
	for _, x := range all {
		keys = append(keys, x.Key)
	}
	sort.Strings(keys)
	if strings.Join(keys, ",") != "creationDate,only,owner" {
		t.Fatalf("%v", keys)
	}
	if err := cl.OverwriteTags(ctx, id, nil); err == nil || !strings.Contains(err.Error(), "At least one tag must be given.") {
		t.Fatalf("empty overwrite must be rejected: %v", err)
	}
	if err := cl.DeleteTag(ctx, id, "only"); err != nil {
		t.Fatal(err)
	}
	if _, err := cl.GetTag(ctx, id, "only"); err == nil || !strings.HasPrefix(err.Error(), fmt.Sprintf(FetchingTagWithKeyFailed, "only", id)) {
		t.Fatalf("%v", err)
	}
	if err := cl.DeleteTag(ctx, id, "only"); err == nil {
		t.Fatal("second delete must fail")
	}
	if _, err := cl.GetTags(ctx, uuid.New()); err == nil || !strings.HasPrefix(err.Error(), "Fetching tags for secret #") {
		t.Fatalf("%v", err)
	}
}

func TestContractListFilterSortPage(t *testing.T) {
	c, srv := newCluster(t, 2)
	cl := c.newClient(srv)
	ctx := context.Background()
	ids := map[int]uuid.UUID{}
	for i := 1; i <= 5; i++ {
		s := NewSecret([]Tag{{Key: "n", Value: strconv.Itoa(i * 10), ValueType: TagLong}, {Key: "parity", Value: []string{"even", "odd"}[i%2]}}, big64(int64(i)))
		if _, err := cl.CreateSecret(ctx, s); err != nil {
			t.Fatal(err)
		}
		ids[i] = s.SecretID
	}
	nOf := func(ms []Metadata) []string {
		var out []string
		for _, m := range ms {
			for _, tg := range m.Tags {
				if tg.Key == "n" {
					out = append(out, tg.Value)
				}
			}
		}
		return out
	}
	all, err := cl.GetSecrets(ctx, nil, nil)
	if err != nil || len(all) != 5 {
		t.Fatal(len(all), err)
	}
	asc, err := cl.GetSecrets(ctx, nil, &Sort{Property: "n"})
	if err != nil || strings.Join(nOf(asc), ",") != "10,20,30,40,50" {
		t.Fatal(nOf(asc), err)
	}
	desc, _ := cl.GetSecrets(ctx, nil, &Sort{Property: "n", Desc: true})
	if strings.Join(nOf(desc), ",") != "50,40,30,20,10" {
		t.Fatal(nOf(desc))
	}
	f1, err := cl.GetSecrets(ctx, []TagFilter{{Key: "n", Operator: OpGreaterThan, Value: "20"}, {Key: "parity", Operator: OpEquals, Value: "odd"}}, &Sort{Property: "n"})
	if err != nil || strings.Join(nOf(f1), ",") != "30,50" {
		t.Fatal(nOf(f1), err)
	}
	f2, _ := cl.GetSecrets(ctx, []TagFilter{{Key: "n", Operator: OpLessThan, Value: "30"}}, &Sort{Property: "n"})
	if strings.Join(nOf(f2), ",") != "10,20" {
		t.Fatal(nOf(f2))
	}
	// non-ASCII filter values survive the double URL encoding (client escapes the part, then the query)
	u := NewSecret([]Tag{{Key: "word", Value: "\u00e4\u00f6"}}, big64(9))
	if _, err := cl.CreateSecret(ctx, u); err != nil {
		t.Fatal(err)
	}
	if got, err := cl.GetSecrets(ctx, []TagFilter{{Key: "word", Operator: OpEquals, Value: "\u00e4\u00f6"}}, nil); err != nil || len(got) != 1 || got[0].SecretID != u.SecretID.String() {
		t.Fatal(got, err)
	}
	if err := cl.DeleteSecret(ctx, u.SecretID); err != nil {
		t.Fatal(err)
	}
	// a space in a filter value is not accepted by the service's filter grammar (\w - . % only): the client's '+' is rejected
	if _, err := cl.GetSecrets(ctx, []TagFilter{{Key: "parity", Operator: OpEquals, Value: "a b"}}, nil); err == nil || !strings.Contains(err.Error(), "400") {
		t.Fatalf("%v", err)
	}
	if _, err := cl.GetSecrets(ctx, []TagFilter{{Key: "bad key!", Operator: OpEquals, Value: "x"}}, nil); err == nil || !strings.HasPrefix(err.Error(), FetchingSecretMetadataFails) {
		t.Fatalf("%v", err)
	}
	pg, err := cl.GetSecretsPage(ctx, nil, PageRequest{Page: 1, Size: 2, Sort: &Sort{Property: "n"}})
	if err != nil || pg.Number != 1 || pg.Size != 2 || pg.TotalElements != 5 || pg.TotalPages != 3 || strings.Join(nOf(pg.Content), ",") != "30,40" {
		t.Fatalf("%+v %v", pg, err)
	}
	last, _ := cl.GetSecretsPage(ctx, nil, PageRequest{Page: 2, Size: 2, Sort: &Sort{Property: "n"}})
	if strings.Join(nOf(last.Content), ",") != "50" {
		t.Fatal(nOf(last.Content))
	}
	past, err := cl.GetSecretsPage(ctx, nil, PageRequest{Page: 9, Size: 2})
	if err != nil || len(past.Content) != 0 || past.TotalElements != 5 {
		t.Fatalf("%+v %v", past, err)
	}
	filtered, _ := cl.GetSecretsPage(ctx, []TagFilter{{Key: "parity", Operator: OpEquals, Value: "even"}}, PageRequest{Page: 0, Size: 1})
	if filtered.TotalElements != 2 || filtered.TotalPages != 2 || len(filtered.Content) != 1 {
		t.Fatalf("%+v", filtered)
	}
}

func TestContractIntraAndInterVcp(t *testing.T) {
	c, srv := newCluster(t, 2)
	ctx := context.Background()
	intra, err := NewIntraVcpClient(Options{ServiceAddresses: []string{srv[0].URL}})
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.New()
	data := make([]byte, 2*spdz.ShareWidth)
	for i := range data {
		data[i] = byte(i)
	}
	got, err := intra.UploadSecretShare(ctx, SecretShare{SecretID: id, Tags: []Tag{{Key: "k", Value: "v"}}, Data: data})
	if err != nil || got != id {
		t.Fatal(got, err)
	}
	// Known wire mismatch inherited from the Java implementation: the service requires the programId query parameter
	// of GET /intra-vcp/secret-shares/{id} but IntraVcpClient.GetSecretShare does not send it, so the real server
	// answers 400. Pinned here so that a change on either side is noticed.
	if _, err := intra.GetSecretShare(ctx, id); err == nil || !strings.Contains(err.Error(), "400") {
		t.Fatalf("expected the 400 for the missing programId, got %v", err)
	}
	// with the parameter the download returns what was uploaded, in the format the client decodes
	resp, err := http.Get(srv[0].URL + "/intra-vcp/secret-shares/" + id.String() + "?programId=prog")
	if err != nil || resp.StatusCode != 200 {
		t.Fatal(resp, err)
	}
	var sj shareJSON
	if err := json.NewDecoder(resp.Body).Decode(&sj); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if sj.SecretID != id.String() || string(sj.Data) != string(data) || len(sj.Tags) < 1 || sj.Tags[0].Key != "k" {
		t.Fatalf("%+v", sj)
	}
	if _, err := intra.UploadSecretShare(ctx, SecretShare{SecretID: id, Data: data}); err == nil || !strings.Contains(err.Error(), "409") {
		t.Fatalf("duplicate upload: %v", err)
	}
	if _, err := intra.UploadSecretShare(ctx, SecretShare{SecretID: uuid.New(), Data: data[:5]}); err == nil || !strings.Contains(err.Error(), "400") {
		t.Fatalf("bad width: %v", err)
	}

	inter, err := NewInterVcpClient(Options{ServiceAddresses: []string{srv[0].URL, srv[1].URL}})
	if err != nil {
		t.Fatal(err)
	}
	big1, _ := new(big.Int).SetString("-123456789012345678901234567890123456789012345678901234567890", 10)
	op := uuid.New()
	err = inter.Open(ctx, MultiplicationExchangeObject{OperationID: op, PlayerID: 1, InterimValues: []FactorPair{{A: big1, B: big.NewInt(0)}, {A: big.NewInt(5), B: c.sp.Prime}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range c.parties {
		if len(p.interim) != 1 || p.interim[0].OperationID != op.String() || p.interim[0].PlayerID != 1 || len(p.interim[0].Values) != 2 ||
			p.interim[0].Values[0].A.Cmp(big1) != 0 || p.interim[0].Values[1].B.Cmp(c.sp.Prime) != 0 {
			t.Fatalf("party %d: %+v", p.idx, p.interim)
		}
	}
	c.parties[1].failing = true
	err = inter.Open(ctx, MultiplicationExchangeObject{OperationID: uuid.New(), PlayerID: 0})
	if err == nil || !strings.HasPrefix(err.Error(), AtLeastOneFailedPrefix) || !strings.Contains(err.Error(), srv[1].URL) {
		t.Fatalf("%v", err)
	}
	// an empty value list is valid
	c.parties[1].failing = false
	if err := inter.Open(ctx, MultiplicationExchangeObject{OperationID: uuid.New(), PlayerID: 0}); err != nil {
		t.Fatal(err)
	}
}

func TestContractServiceStaysCompatibleWithClientTokens(t *testing.T) {
	// a client without a token provider is rejected by the user facing API (400: missing Authorization header)
	c, srv := newCluster(t, 1)
	o := c.options(srv)
	o.BearerTokenProvider = nil
	cl, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cl.GetTags(context.Background(), uuid.New()); err == nil || !strings.Contains(err.Error(), "400") {
		t.Fatalf("%v", err)
	}
}
