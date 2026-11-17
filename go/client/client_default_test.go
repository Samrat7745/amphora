package client

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Samrat7745/amphora/go/spdz"
	"github.com/google/uuid"
)

// Port of DefaultAmphoraClientTest and DefaultAmphoraClientBuilderTest (Options + New replace the
// builder). The Java tests mock the communication client; here two httptest parties stand in for
// the Amphora services.

const testSecretID = "3bcf8308-8f50-4d24-a37b-b0075bb5e779"

var (
	tid       = uuid.MustParse(testSecretID)
	testTag   = Tag{Key: "key1", Value: "tag1"}
	twoTags   = []Tag{{Key: "key1", Value: "tag1"}, {Key: "key2", Value: "tag2"}}
	listPage  = `{"content":[{"secretId":"52bd51d2-f73d-404a-a326-3af9e76b6c38","tags":[{"key":"key1","value":"tag1","valueType":"STRING"},{"key":"key2","value":"tag2","valueType":"STRING"}]},{"secretId":"03ad388b-0ea2-44ef-98a3-a4cb2e686dfa","tags":[{"key":"key1","value":"tag1","valueType":"STRING"},{"key":"key2","value":"tag2","valueType":"STRING"}]}],"number":0,"size":2,"totalElements":2,"totalPages":1}`
	listCount = 2
)

func twoParties(t *testing.T, h func(w http.ResponseWriter, r *http.Request, body []byte)) (*party, *party) {
	return newParty(t, h), newParty(t, h)
}

// ---- builder ----

// givenEndpointsNotDefined / givenPrimeOmitted / givenRInvOmitted / givenROmitted
func TestBuilderValidation(t *testing.T) {
	sp := params()
	for _, tc := range []struct {
		name string
		o    Options
		want string
	}{
		{"endpoints not defined", Options{Prime: sp.Prime, R: sp.R, RInv: sp.RInv}, "At least one amphora service uri has to be provided."},
		{"prime omitted", Options{ServiceAddresses: []string{"https://amphora.carbynestack.io:80"}, R: sp.R, RInv: sp.RInv}, "Prime must not be null"},
		{"rInv omitted", Options{ServiceAddresses: []string{"https://amphora.carbynestack.io:80"}, Prime: sp.Prime, R: sp.R}, "Multiplicative inverse for the auxiliary modulus R must not be null"},
		{"r omitted", Options{ServiceAddresses: []string{"https://amphora.carbynestack.io:80"}, Prime: sp.Prime, RInv: sp.RInv}, "Auxiliary modulus R must not be null"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := New(tc.o)
			if err == nil || err.Error() != tc.want || c != nil {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
		})
	}
	// an invalid service address is rejected too
	if _, err := New(opts("")); err == nil {
		t.Fatal("empty address must fail")
	}
}

// givenValidConfiguration_whenBuildingClient_thenCallConstructorWithBuilder: all configured values
// are used by the client: every endpoint is contacted with its own token.
func TestNewUsesAllConfiguration(t *testing.T) {
	p1, p2 := twoParties(t, jsonReply([]Tag{}))
	p3 := newParty(t, jsonReply([]Tag{}))
	o := optsFor(p1, p2, p3)
	o.BearerTokenProvider = func(addr string) string { return "tok:" + addr }
	c, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.CreateTag(context.Background(), tid, testTag); err != nil {
		t.Fatal(err)
	}
	for _, p := range []*party{p1, p2, p3} {
		if r := p.only(t); r.Auth != "Bearer tok:"+p.url() || r.Method != "POST" {
			t.Fatalf("%+v", r)
		}
	}
}

// ...WithoutSslCertificateValidation / addTrustedCertificate and givenInvalidCertFile_whenCreatingClient_thenThrowException
func TestTLSConfiguration(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`[]`)) }))
	defer srv.Close()
	dir := t.TempDir()
	certFile := filepath.Join(dir, "server.pem")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	call := func(o Options) error {
		c, err := New(o)
		if err != nil {
			t.Fatal(err)
		}
		_, err = c.GetTags(context.Background(), tid)
		return err
	}
	if err := call(opts(srv.URL)); err == nil {
		t.Fatal("an untrusted certificate must be rejected")
	}
	o := opts(srv.URL)
	o.NoSSLValidation = true
	if err := call(o); err != nil {
		t.Fatalf("withoutSslCertificateValidation: %v", err)
	}
	o = opts(srv.URL)
	o.TrustedCertificates = []string{certFile}
	if err := call(o); err != nil {
		t.Fatalf("trusted certificate: %v", err)
	}
	// invalid certificate files fail the construction
	for name, content := range map[string]string{"empty": "", "garbage": "not a pem"} {
		bad := filepath.Join(dir, name+".pem")
		_ = os.WriteFile(bad, []byte(content), 0o600)
		o = opts(srv.URL)
		o.TrustedCertificates = []string{bad}
		_, err := New(o)
		ce, ok := err.(*ClientError)
		if !ok || ce.Msg != "Failed to create AmphoraCommunicationClient." || ce.Cause == nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	o = opts(srv.URL)
	o.TrustedCertificates = []string{filepath.Join(dir, "missing.pem")}
	if _, err := New(o); err == nil {
		t.Fatal("missing certificate file must fail")
	}
}

// ---- secret sharing ----

func shareData(t *testing.T, sp *spdz.Utils, n int) []*big.Int {
	secrets := make([]*big.Int, n)
	for i := range secrets {
		v, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
		secrets[i] = v
	}
	return secrets
}

func odoJSON(o odo) map[string]any {
	return map[string]any{"secretShares": o.SecretShares, "rShares": o.RShares, "vShares": o.VShares, "wShares": o.WShares, "uShares": o.UShares}
}

// inputMaskParties serves the input mask ODOs of two parties for the given masks.
func inputMaskParties(t *testing.T, sp *spdz.Utils, masks []*big.Int) (*party, *party) {
	odos := fakeODOs(t, sp, masks, 2, false)
	h := func(i int) func(w http.ResponseWriter, r *http.Request, body []byte) {
		return func(w http.ResponseWriter, r *http.Request, body []byte) {
			if r.Method == "GET" && r.URL.Path == "/input-masks" {
				_ = json.NewEncoder(w).Encode(odoJSON(odos[i]))
				return
			}
			w.WriteHeader(201)
		}
	}
	return newParty(t, h(0)), newParty(t, h(1))
}

// givenSecretsOfVariousSize_whenSharingAndRecombining_thenRecoverInitialData
func TestCreateSecretRecoversInitialData(t *testing.T) {
	sp := params()
	for i := 0; i < 25; i++ {
		n, _ := rand.Int(rand.Reader, big.NewInt(1000))
		size := int(n.Int64()) + 1
		secrets := shareData(t, sp, size)
		seen := make([]*big.Int, size)
		for j := range seen {
			seen[j] = rnd(sp.Prime)
		}
		p1, p2 := inputMaskParties(t, sp, seen)
		c, err := New(optsFor(p1, p2))
		if err != nil {
			t.Fatal(err)
		}
		s := NewSecret(twoTags, secrets)
		id, err := c.CreateSecret(context.Background(), s)
		if err != nil || id != s.SecretID {
			t.Fatalf("size %d: %v", size, err)
		}
		// one input mask request per service with the secret id as request id and the element count
		var maskReq, postReq []recReq
		for _, r := range p1.requests() {
			if r.Path == "/input-masks" {
				maskReq = append(maskReq, r)
			} else {
				postReq = append(postReq, r)
			}
		}
		if len(maskReq) != 1 || len(postReq) != 1 || postReq[0].Path != "/masked-inputs" || postReq[0].Method != "POST" {
			t.Fatalf("%+v %+v", maskReq, postReq)
		}
		q, _ := url.ParseQuery(maskReq[0].RawQuery)
		if q.Get("requestId") != s.SecretID.String() || q.Get("count") != fmt.Sprint(size) {
			t.Fatalf("input mask query %q", maskReq[0].RawQuery)
		}
		// both services receive the identical masked input
		if string(p1.requests()[len(p1.requests())-1].Body) != string(p2.requests()[len(p2.requests())-1].Body) {
			t.Fatal("masked inputs differ between services")
		}
		var mi struct {
			SecretID string `json:"secretId"`
			Data     []struct {
				Value []byte `json:"value"`
			} `json:"data"`
			Tags []Tag `json:"tags"`
		}
		if err := json.Unmarshal(postReq[0].Body, &mi); err != nil {
			t.Fatal(err)
		}
		if mi.SecretID != s.SecretID.String() || len(mi.Data) != size || len(mi.Tags) != 2 || mi.Tags[1].ValueType != "STRING" {
			t.Fatalf("%+v", mi)
		}
		for j := range secrets {
			masked, err := sp.FromGfp(mi.Data[j].Value)
			if err != nil {
				t.Fatal(err)
			}
			rec := new(big.Int).Mod(new(big.Int).Add(seen[j], masked), sp.Prime)
			if rec.Cmp(secrets[j]) != 0 {
				t.Fatalf("size %d element %d: %v != %v", size, j, rec, secrets[j])
			}
		}
	}
}

// givenUploadingMaskedInputFails_whenCreateObject_thenThrowAmphoraClientException
func TestCreateSecretUploadFails(t *testing.T) {
	sp := params()
	odos := fakeODOs(t, sp, []*big.Int{rnd(sp.Prime)}, 2, false)
	mk := func(i int, failUpload bool) *party {
		return newParty(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
			if r.URL.Path == "/input-masks" {
				_ = json.NewEncoder(w).Encode(odoJSON(odos[i]))
				return
			}
			if failUpload {
				http.Error(w, "500", 500)
				return
			}
			w.WriteHeader(201)
		})
	}
	bad, good := mk(0, true), mk(1, false)
	c, _ := New(optsFor(bad, good))
	_, err := c.CreateSecret(context.Background(), NewSecret(nil, []*big.Int{big.NewInt(42)}))
	ce, ok := err.(*ClientError)
	if !ok || !strings.HasPrefix(ce.Msg, CreateFailedPrefix) || !strings.Contains(ce.Msg, `Request for endpoint "`+bad.url()+`" failed: HTTP 500`) || strings.Contains(ce.Msg, good.url()) {
		t.Fatalf("%v", err)
	}
}

func TestCreateSecretInputMaskDownloadFails(t *testing.T) {
	sp := params()
	odos := fakeODOs(t, sp, []*big.Int{rnd(sp.Prime)}, 2, false)
	good := newParty(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		_ = json.NewEncoder(w).Encode(odoJSON(odos[0]))
	})
	bad := newParty(t, failWith(500, "no masks"))
	c, _ := New(optsFor(good, bad))
	_, err := c.CreateSecret(context.Background(), NewSecret(nil, []*big.Int{big.NewInt(1)}))
	if err == nil || !strings.HasPrefix(err.Error(), ResponsesFailedPrefix) || !strings.Contains(err.Error(), "no masks") {
		t.Fatalf("%v", err)
	}
	if len(good.requests()) != 1 {
		t.Fatal("no masked input may be uploaded")
	}
}

func verifiableJSON(o odo, id string, tags []Tag) map[string]any {
	m := odoJSON(o)
	m["secretId"] = id
	m["tags"] = tags
	return m
}

// givenOutputDeliveryObjectsOfVariousSize_whenDownloadingObject_thenReturnExpectedContent
func TestGetSecretReturnsRecombinedContent(t *testing.T) {
	sp := params()
	for i := 0; i < 25; i++ {
		n, _ := rand.Int(rand.Reader, big.NewInt(1000))
		secrets := shareData(t, sp, int(n.Int64())+1)
		odos := fakeODOs(t, sp, secrets, 2, false)
		mk := func(i int) *party {
			return newParty(t, jsonReply(verifiableJSON(odos[i], testSecretID, twoTags)))
		}
		p1, p2 := mk(0), mk(1)
		c, _ := New(optsFor(p1, p2))
		got, err := c.GetSecret(context.Background(), tid)
		if err != nil || len(got.Data) != len(secrets) || got.SecretID != tid || len(got.Tags) != 2 {
			t.Fatalf("%v", err)
		}
		for j := range secrets {
			if got.Data[j].Cmp(secrets[j]) != 0 {
				t.Fatalf("element %d differs", j)
			}
		}
		// the same fresh request id goes to every service
		q1, q2 := p1.only(t), p2.only(t)
		v1, _ := url.ParseQuery(q1.RawQuery)
		v2, _ := url.ParseQuery(q2.RawQuery)
		if _, err := uuid.Parse(v1.Get("requestId")); err != nil || v1.Get("requestId") != v2.Get("requestId") || q1.Path != "/secret-shares/"+testSecretID {
			t.Fatalf("%+v %+v", q1, q2)
		}
	}
}

// givenDownloadDataFromOnePlayerFails_whenDownloadingObject_thenThrowAmphoraClientException
func TestGetSecretOnePlayerFails(t *testing.T) {
	sp := params()
	odos := fakeODOs(t, sp, []*big.Int{big.NewInt(42), big.NewInt(24)}, 2, false)
	ok := newParty(t, jsonReply(verifiableJSON(odos[0], testSecretID, twoTags)))
	bad := newParty(t, failWith(500, "Call failed"))
	c, _ := New(optsFor(ok, bad))
	_, err := c.GetSecret(context.Background(), tid)
	if err == nil || !strings.HasPrefix(err.Error(), "Error(s) occurred while processing responses") || !strings.Contains(err.Error(), "Call failed") {
		t.Fatalf("%v", err)
	}
}

func TestGetSecretDetectsTamperedShares(t *testing.T) {
	sp := params()
	odos := fakeODOs(t, sp, []*big.Int{big.NewInt(42)}, 2, true)
	p1 := newParty(t, jsonReply(verifiableJSON(odos[0], testSecretID, nil)))
	p2 := newParty(t, jsonReply(verifiableJSON(odos[1], testSecretID, nil)))
	c, _ := New(optsFor(p1, p2))
	if _, err := c.GetSecret(context.Background(), tid); err == nil {
		t.Fatal("must fail")
	} else if _, ok := err.(*IntegrityError); !ok {
		t.Fatalf("%T %v", err, err)
	}
}

// ---- listing ----

func TestListingConstructsExpectedRequest(t *testing.T) {
	asc := &Sort{Property: "key1"}
	desc := &Sort{Property: "key1", Desc: true}
	// expected raw queries use the Java parameter order and encoding
	for _, tc := range []struct {
		name    string
		filters []TagFilter
		sort    *Sort
		page    *PageRequest
		query   string
	}{
		{"no filter", nil, nil, nil, ""},
		{"equals filter", []TagFilter{{"key", OpEquals, "value"}}, nil, nil, "filter=key%3Avalue"},
		{"less than filter", []TagFilter{{"key", OpLessThan, "123"}}, nil, nil, "filter=key%3C123"},
		{"two filters", []TagFilter{{"key", OpEquals, "value,"}, {"key_2", OpLessThan, "123"}}, nil, nil, "filter=key%3Avalue%252C%2Ckey_2%3C123"},
		{"filter and empty value", []TagFilter{{"key", OpEquals, ""}, {"key_2", OpLessThan, "123"}}, nil, nil, "filter=key%3A%2Ckey_2%3C123"},
		{"pagination", nil, nil, &PageRequest{Page: 3, Size: 50}, "pageNumber=3&pageSize=50"},
		{"pagination and sorting", nil, nil, &PageRequest{Page: 3, Size: 50, Sort: asc}, "pageNumber=3&pageSize=50&sort=key1&dir=ASC"},
		{"sorting", nil, asc, nil, "sort=key1&dir=ASC"},
		{"filter and sorting", []TagFilter{{"key", OpLessThan, "123"}}, desc, nil, "filter=key%3C123&sort=key1&dir=DESC"},
		{"filters, pagination and sorting", []TagFilter{{"key", OpLessThan, "123"}}, nil, &PageRequest{Page: 3, Size: 50, Sort: desc}, "filter=key%3C123&pageNumber=3&pageSize=50&sort=key1&dir=DESC"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p1 := newParty(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) { _, _ = w.Write([]byte(listPage)) })
			p2 := newParty(t, failWith(500, "must not be asked"))
			c, _ := New(optsFor(p1, p2))
			var content []Metadata
			if tc.page != nil {
				pg, err := c.GetSecretsPage(context.Background(), tc.filters, *tc.page)
				if err != nil {
					t.Fatal(err)
				}
				content = pg.Content
				if pg.TotalElements != 2 || pg.TotalPages != 1 || pg.Size != 2 {
					t.Fatalf("%+v", pg)
				}
			} else {
				var err error
				if content, err = c.GetSecrets(context.Background(), tc.filters, tc.sort); err != nil {
					t.Fatal(err)
				}
			}
			if len(content) != listCount || content[0].SecretID != "52bd51d2-f73d-404a-a326-3af9e76b6c38" || len(content[0].Tags) != 2 || content[1].Tags[1].Key != "key2" {
				t.Fatalf("%+v", content)
			}
			r := p1.only(t)
			if r.Method != "GET" || r.Path != "/secret-shares" || r.RawQuery != tc.query {
				t.Fatalf("got %s %s?%s, want ?%s", r.Method, r.Path, r.RawQuery, tc.query)
			}
			if len(p2.requests()) != 0 {
				t.Fatal("only the first service is queried for metadata")
			}
		})
	}
}

func TestListingFailure(t *testing.T) {
	p := newParty(t, failWith(500, "boom"))
	c, _ := New(optsFor(p))
	_, err := c.GetSecrets(context.Background(), nil, nil)
	ce, ok := err.(*ClientError)
	if !ok || ce.Msg != "Fetching secret metadata failed" || ce.Cause == nil {
		t.Fatalf("%v", err)
	}
}

// ---- tags ----

// givenSuccessfulRequest_whenListTags_thenReturnExpectedResult
func TestGetTags(t *testing.T) {
	p1 := newParty(t, jsonReply(twoTags))
	p2 := newParty(t, jsonReply(twoTags))
	c, _ := New(optsFor(p1, p2))
	got, err := c.GetTags(context.Background(), tid)
	if err != nil || len(got) != 2 || got[0].Key != "key1" || got[1].Key != "key2" || got[1].ValueType != "STRING" && got[1].ValueType != "" {
		t.Fatalf("%v %v", got, err)
	}
	// one of the services is asked
	n := 0
	for _, p := range []*party{p1, p2} {
		for _, r := range p.requests() {
			n++
			if r.Method != "GET" || r.Path != "/secret-shares/"+testSecretID+"/tags" {
				t.Fatalf("%+v", r)
			}
		}
	}
	if n != 1 {
		t.Fatalf("%d requests", n)
	}
}

// givenCommunicationClientFails_whenGetTag_thenThrowAmphoraClientException
func TestGetTagFailure(t *testing.T) {
	p := newParty(t, failWith(404, "nope"))
	c, _ := New(optsFor(p))
	_, err := c.GetTag(context.Background(), tid, "tagKey")
	ce, ok := err.(*ClientError)
	if !ok || ce.Msg != fmt.Sprintf(`Fetching tag with key "tagKey" for secret #%s failed`, testSecretID) {
		t.Fatalf("%v", err)
	}
	_, err = c.GetTags(context.Background(), tid)
	if ce, ok := err.(*ClientError); !ok || ce.Msg != fmt.Sprintf("Fetching tags for secret #%s failed", testSecretID) {
		t.Fatalf("%v", err)
	}
}

// givenSuccessfulRequest_whenGetTag_thenReturnExpectedResult
func TestGetTag(t *testing.T) {
	p := newParty(t, jsonReply(testTag))
	c, _ := New(optsFor(p))
	got, err := c.GetTag(context.Background(), tid, testTag.Key)
	if err != nil || got.Key != testTag.Key || got.Value != testTag.Value {
		t.Fatalf("%v %v", got, err)
	}
	if r := p.only(t); r.Method != "GET" || r.Path != "/secret-shares/"+testSecretID+"/tags/key1" {
		t.Fatalf("%+v", r)
	}
}

func tagBody(t *testing.T, r recReq) Tag {
	t.Helper()
	var tg Tag
	if err := json.Unmarshal(r.Body, &tg); err != nil {
		t.Fatal(err)
	}
	return tg
}

// givenSuccessfulRequest_whenCreateTag_thenExecuteExpectedRequest
func TestCreateTag(t *testing.T) {
	p1, p2 := twoParties(t, okEmpty)
	c, _ := New(optsFor(p1, p2))
	if err := c.CreateTag(context.Background(), tid, testTag); err != nil {
		t.Fatal(err)
	}
	for _, p := range []*party{p1, p2} {
		r := p.only(t)
		if r.Method != "POST" || r.Path != "/secret-shares/"+testSecretID+"/tags" || tagBody(t, r) != (Tag{"key1", "tag1", "STRING"}) {
			t.Fatalf("%+v %s", r, r.Body)
		}
	}
}

// givenRequestReturnsFailure_whenCreateTag_thenThrowAmphoraClientException
func TestCreateTagFailure(t *testing.T) {
	bad, good := newParty(t, failWith(500, "500")), newParty(t, okEmpty)
	c, _ := New(optsFor(bad, good))
	err := c.CreateTag(context.Background(), tid, testTag)
	if err == nil || !strings.Contains(err.Error(), `Request for endpoint "`+bad.url()+`" failed: HTTP 500`) {
		t.Fatalf("%v", err)
	}
	if len(good.requests()) != 1 {
		t.Fatal("the other service is still called")
	}
}

// givenSuccessfulRequest_whenOverwriteTags_thenPerformExpectedRequest
func TestOverwriteTags(t *testing.T) {
	p1, p2 := twoParties(t, okEmpty)
	c, _ := New(optsFor(p1, p2))
	if err := c.OverwriteTags(context.Background(), tid, []Tag{testTag}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []*party{p1, p2} {
		r := p.only(t)
		var tags []Tag
		if err := json.Unmarshal(r.Body, &tags); err != nil || r.Method != "PUT" || r.Path != "/secret-shares/"+testSecretID+"/tags" || len(tags) != 1 || tags[0].Key != "key1" {
			t.Fatalf("%+v", r)
		}
	}
}

// givenSuccessfulRequest_whenUpdateTag_thenPerformExpectedRequest
func TestUpdateTag(t *testing.T) {
	p1, p2 := twoParties(t, okEmpty)
	c, _ := New(optsFor(p1, p2))
	if err := c.UpdateTag(context.Background(), tid, testTag); err != nil {
		t.Fatal(err)
	}
	for _, p := range []*party{p1, p2} {
		r := p.only(t)
		if r.Method != "PUT" || r.Path != "/secret-shares/"+testSecretID+"/tags/key1" || tagBody(t, r).Value != "tag1" {
			t.Fatalf("%+v", r)
		}
	}
}

// givenSuccessfulRequest_whenDeleteTag_thenPerformExpectedRequest
func TestDeleteTag(t *testing.T) {
	p1, p2 := twoParties(t, okEmpty)
	c, _ := New(optsFor(p1, p2))
	if err := c.DeleteTag(context.Background(), tid, "key1"); err != nil {
		t.Fatal(err)
	}
	for _, p := range []*party{p1, p2} {
		if r := p.only(t); r.Method != "DELETE" || r.Path != "/secret-shares/"+testSecretID+"/tags/key1" {
			t.Fatalf("%+v", r)
		}
	}
}

// givenRequestReturnsFailure_whenDeleteTag_thenThrowAmphoraClientException (the Java test deletes the secret)
func TestDeleteSecretFailure(t *testing.T) {
	bad, good := newParty(t, failWith(404, "Secret Not found")), newParty(t, okEmpty)
	c, _ := New(optsFor(bad, good))
	err := c.DeleteSecret(context.Background(), tid)
	if err == nil || !strings.HasPrefix(err.Error(), "At least one request has failed") || !strings.Contains(err.Error(), "Secret Not found") {
		t.Fatalf("%v", err)
	}
}

func TestDeleteSecret(t *testing.T) {
	p1, p2 := twoParties(t, okEmpty)
	c, _ := New(optsFor(p1, p2))
	if err := c.DeleteSecret(context.Background(), tid); err != nil {
		t.Fatal(err)
	}
	for _, p := range []*party{p1, p2} {
		if r := p.only(t); r.Method != "DELETE" || r.Path != "/secret-shares/"+testSecretID {
			t.Fatalf("%+v", r)
		}
	}
}
