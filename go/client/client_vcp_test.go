package client

import (
	"context"
	"encoding/json"
	"math/big"
	"net/http"
	"strings"
	"testing"

	"github.com/Samrat7745/amphora/go/spdz"
	"github.com/google/uuid"
)

// Ports of DefaultAmphoraIntraVcpClientTest, DefaultAmphoraInterVcpClientTest and SecretShareUtilTest.

func testShare(t *testing.T, id uuid.UUID) SecretShare {
	t.Helper()
	sp := params()
	v, err := sp.ToGfp(big.NewInt(123456789))
	if err != nil {
		t.Fatal(err)
	}
	return SecretShare{SecretID: id, Tags: twoTags, Data: append(v, make([]byte, spdz.WordWidth)...)}
}

// ---- intra VCP ----

// givenNoServiceUriDefined_whenBuildingInterVcpClient_thenThrowException
func TestIntraClientNoServiceURI(t *testing.T) {
	c, err := NewIntraVcpClient(Options{})
	if err == nil || c != nil || err.Error() != "Service URI must not be null." {
		t.Fatalf("%v", err)
	}
}

// givenShareIsValid_whenUploadSecretShare_thenSucceed
func TestIntraClientUploadSecretShare(t *testing.T) {
	p := newParty(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) { w.WriteHeader(201) })
	c, err := NewIntraVcpClient(optsFor(p))
	if err != nil {
		t.Fatal(err)
	}
	share := testShare(t, tid)
	id, err := c.UploadSecretShare(context.Background(), share)
	if err != nil || id != tid {
		t.Fatal(id, err)
	}
	r := p.only(t)
	var body struct {
		SecretID string `json:"secretId"`
		Tags     []Tag  `json:"tags"`
		Data     []byte `json:"data"`
	}
	if err := json.Unmarshal(r.Body, &body); err != nil || r.Method != "POST" || r.Path != "/intra-vcp/secret-shares" ||
		body.SecretID != testSecretID || len(body.Tags) != 2 || string(body.Data) != string(share.Data) || r.Auth != "" {
		t.Fatalf("%+v %s", r, r.Body)
	}
}

func TestIntraClientUploadFailure(t *testing.T) {
	p := newParty(t, failWith(409, "exists"))
	c, _ := NewIntraVcpClient(optsFor(p))
	_, err := c.UploadSecretShare(context.Background(), testShare(t, tid))
	if ce, ok := err.(*ClientError); !ok || ce.Msg != "Creating SecretShare failed" || ce.Cause == nil {
		t.Fatalf("%v", err)
	}
}

// givenIdIsValid_whenDownloadingSecretShare_thenReturnSecretShare
func TestIntraClientGetSecretShare(t *testing.T) {
	share := testShare(t, tid)
	p := newParty(t, jsonReply(map[string]any{"secretId": testSecretID, "tags": share.Tags, "data": share.Data}))
	c, _ := NewIntraVcpClient(optsFor(p))
	got, err := c.GetSecretShare(context.Background(), tid)
	if err != nil || got.SecretID != tid || string(got.Data) != string(share.Data) || len(got.Tags) != 2 || got.Tags[0] != (Tag{"key1", "tag1", "STRING"}) {
		t.Fatalf("%+v %v", got, err)
	}
	if r := p.only(t); r.Method != "GET" || r.Path != "/intra-vcp/secret-shares/"+testSecretID || r.Auth != "" {
		t.Fatalf("%+v", r)
	}
}

// givenSecretShareWithIdDoesNotExist_whenDownloadingSecretShare_thenThrowException
func TestIntraClientGetUnknownSecretShare(t *testing.T) {
	p := newParty(t, failWith(404, "Secret with ID does not exist."))
	c, _ := NewIntraVcpClient(optsFor(p))
	_, err := c.GetSecretShare(context.Background(), tid)
	ce, ok := err.(*ClientError)
	if !ok || ce.Msg != "Fetching secret #"+testSecretID+" failed" || !strings.Contains(ce.Cause.Error(), "Secret with ID does not exist.") {
		t.Fatalf("%v", err)
	}
}

// ---- inter VCP ----

func exchangeObject(id uuid.UUID) MultiplicationExchangeObject {
	return MultiplicationExchangeObject{OperationID: id, PlayerID: 0, InterimValues: []FactorPair{{A: big.NewInt(1), B: big.NewInt(10)}}}
}

// givenNoServiceUriDefined_whenBuildingInterVcClient_thenThrowException
func TestInterClientNoServiceURI(t *testing.T) {
	c, err := NewInterVcpClient(Options{})
	if err == nil || c != nil || err.Error() != "At least one amphora service URI needs to be defined." {
		t.Fatalf("%v", err)
	}
}

// givenValidExchangeObject_whenOpeningValues_thenSucceed: every service receives the object
func TestInterClientOpen(t *testing.T) {
	p1, p2 := twoParties(t, okEmpty)
	c, _ := NewInterVcpClient(optsFor(p1, p2))
	opID := uuid.MustParse("72753ecc-464f-42c5-954c-9efa7a5b886c")
	if err := c.Open(context.Background(), exchangeObject(opID)); err != nil {
		t.Fatal(err)
	}
	for _, p := range []*party{p1, p2} {
		r := p.only(t)
		if r.Method != "POST" || r.Path != "/inter-vcp/open" || r.Auth != "" {
			t.Fatalf("%+v", r)
		}
		if string(r.Body) != `{"interimValues":[{"a":1,"b":10}],"operationId":"72753ecc-464f-42c5-954c-9efa7a5b886c","playerId":0}` {
			t.Fatalf("%s", r.Body)
		}
	}
}

// givenOnePlayerCannotBeReached_whenOpeningValues_thenThrowException
func TestInterClientOnePlayerUnreachable(t *testing.T) {
	p1 := newParty(t, okEmpty)
	c, _ := NewInterVcpClient(opts(p1.url(), "http://127.0.0.1:1"))
	err := c.Open(context.Background(), exchangeObject(uuid.New()))
	ce, ok := err.(*ClientError)
	if !ok || !strings.HasPrefix(ce.Msg, AtLeastOneFailedPrefix) || !strings.Contains(ce.Msg, `Request for endpoint "http://127.0.0.1:1" has failed`) || strings.Contains(ce.Msg, p1.url()) {
		t.Fatalf("%v", err)
	}
	p1.only(t)
}

// givenOnePlayerRespondsUnsuccessful_whenOpeningValues_thenThrowException
func TestInterClientOnePlayerRejects(t *testing.T) {
	p1, p2 := newParty(t, okEmpty), newParty(t, failWith(500, "Failure"))
	c, _ := NewInterVcpClient(optsFor(p1, p2))
	err := c.Open(context.Background(), exchangeObject(uuid.New()))
	if err == nil || !strings.Contains(err.Error(), p2.url()) || !strings.Contains(err.Error(), "Failure") {
		t.Fatalf("%v", err)
	}
}

// givenMissingOperationId_whenOpeningValues_thenThrowException
func TestInterClientMissingOperationID(t *testing.T) {
	p := newParty(t, okEmpty)
	c, _ := NewInterVcpClient(optsFor(p))
	err := c.Open(context.Background(), exchangeObject(uuid.Nil))
	if err == nil || !strings.Contains(err.Error(), "OperationId must not be null") {
		t.Fatalf("%v", err)
	}
	if len(p.requests()) != 0 {
		t.Fatal("nothing may be sent")
	}
}

// ---- SecretShareUtil.verifySecrets ----

// odosFor builds the two party ODOs for explicit w/u values (w = s*r, u = v*r unless tampered).
func odosFor(t *testing.T, sp *spdz.Utils, secrets, rs, vs, ws, us []*big.Int) []odo {
	t.Helper()
	odos := make([]odo, 2)
	enc := func(x *big.Int) []byte {
		b, err := sp.ToGfp(new(big.Int).Mod(x, sp.Prime))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	for i := range secrets {
		for _, f := range []struct {
			v   *big.Int
			dst func(o *odo) *[]byte
		}{
			{secrets[i], func(o *odo) *[]byte { return &o.SecretShares }},
			{rs[i], func(o *odo) *[]byte { return &o.RShares }},
			{vs[i], func(o *odo) *[]byte { return &o.VShares }},
			{ws[i], func(o *odo) *[]byte { return &o.WShares }},
			{us[i], func(o *odo) *[]byte { return &o.UShares }},
		} {
			parts := split(sp.Prime, new(big.Int).Mod(f.v, sp.Prime), 2)
			for j := 0; j < 2; j++ {
				d := f.dst(&odos[j])
				*d = append(*d, enc(parts[j])...)
			}
		}
	}
	return odos
}

func verifyFixture(t *testing.T) (sp *spdz.Utils, secrets, rs, vs, ws, us []*big.Int) {
	sp = params()
	n := 5
	rnd63 := func() *big.Int { return new(big.Int).Rsh(rnd(sp.Prime), 65) } // < 2^63 like Math.abs(nextLong())
	for i := 0; i < n; i++ {
		secrets = append(secrets, rnd63())
		rs = append(rs, rnd63())
		vs = append(vs, rnd63())
	}
	for i := 0; i < n; i++ {
		ws = append(ws, new(big.Int).Mul(secrets[i], rs[i]))
		us = append(us, new(big.Int).Mul(vs[i], rs[i]))
	}
	return
}

// givenAllValid_whenVerifyingSecrets_thenDontThrowException
func TestVerifySecretsAllValid(t *testing.T) {
	sp, secrets, rs, vs, ws, us := verifyFixture(t)
	got, err := verify(sp, odosFor(t, sp, secrets, rs, vs, ws, us))
	if err != nil {
		t.Fatal(err)
	}
	for i := range secrets {
		if got[i].Cmp(secrets[i]) != 0 {
			t.Fatalf("element %d", i)
		}
	}
}

// givenWsContainingInvalidValue_whenVerifyingSecrets_thenThrowException
func TestVerifySecretsInvalidW(t *testing.T) {
	sp, secrets, rs, vs, ws, us := verifyFixture(t)
	last := len(ws) - 1
	ws[last] = new(big.Int).Sub(ws[last], big.NewInt(10))
	_, err := verify(sp, odosFor(t, sp, secrets, rs, vs, ws, us))
	ie, ok := err.(*IntegrityError)
	if !ok || !strings.HasPrefix(ie.Msg, "Verification of secret has failed") {
		t.Fatalf("%v", err)
	}
	// the message names the claimed and the actual products like the Java client
	want := new(big.Int).Mod(new(big.Int).Mul(secrets[last], rs[last]), sp.Prime)
	if !strings.Contains(ie.Msg, ws[last].String()+" = "+secrets[last].String()+" * "+rs[last].String()) || !strings.Contains(ie.Msg, ws[last].String()+" = "+want.String()) {
		t.Fatalf("%s", ie.Msg)
	}
	// an invalid u is detected as well
	sp, secrets, rs, vs, ws, us = verifyFixture(t)
	us[0] = new(big.Int).Add(us[0], big.NewInt(1))
	if _, err := verify(sp, odosFor(t, sp, secrets, rs, vs, ws, us)); err == nil {
		t.Fatal("tampered u must be detected")
	}
}
