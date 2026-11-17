package httpapi

// Port of AmphoraServiceSystemTest. The Go service runs in-process behind httptest with the real
// Postgres/Redis/Minio stores (testcontainers-go via internal/testkit; the tests skip when
// docker is unavailable) and a fake OPA that allows the authorized user (the Java test mocks
// OpaClient.isAllowed the same way).

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Samrat7745/amphora/go/internal/jwt"
	"github.com/Samrat7745/amphora/go/internal/objstore"
	"github.com/Samrat7745/amphora/go/internal/opa"
	"github.com/Samrat7745/amphora/go/internal/pgstore"
	"github.com/Samrat7745/amphora/go/internal/rediscache"
	"github.com/Samrat7745/amphora/go/internal/service"
	"github.com/Samrat7745/amphora/go/internal/testkit"
	"github.com/Samrat7745/amphora/go/spdz"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/redis/go-redis/v9"
)

// ---- containers (internal/testkit, testcontainers-go) ----

const (
	minioKey    = testkit.MinioAccessKey
	minioSecret = testkit.MinioSecretKey
)

var (
	sysMu         sync.Mutex
	sysStops      []func()
	sysInfra      *infra
	sysInfraErr   error
	sysInfraReady bool
)

func TestMain(m *testing.M) {
	code := m.Run()
	sysMu.Lock()
	for _, stop := range sysStops {
		stop()
	}
	sysMu.Unlock()
	os.Exit(code)
}

func waitFor(d time.Duration, what string, ready func() bool) error {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if ready() {
			return nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	return fmt.Errorf("%s not ready after %s", what, d)
}

type infra struct {
	pg     *pgstore.Store
	rdb    *redis.Client
	obj    *objstore.Store
	minio  *minio.Client
	bucket string
}

func startInfra() (*infra, error) {
	if !testkit.DockerAvailable() {
		return nil, fmt.Errorf("docker is not available")
	}
	dsn, stop, err := testkit.StartPostgres()
	if err != nil {
		return nil, err
	}
	sysStops = append(sysStops, stop)
	redisAddr, stop, err := testkit.StartRedis()
	if err != nil {
		return nil, err
	}
	sysStops = append(sysStops, stop)
	minioAddr, stop, err := testkit.StartMinio()
	if err != nil {
		return nil, err
	}
	sysStops = append(sysStops, stop)
	mc, err := minio.New(minioAddr, &minio.Options{Creds: credentials.NewStaticV4(minioKey, minioSecret, "")})
	if err != nil {
		return nil, err
	}
	if err := waitFor(60*time.Second, "minio", func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, err := mc.ListBuckets(ctx)
		return err == nil
	}); err != nil {
		return nil, err
	}
	ctx := context.Background()
	in := &infra{minio: mc, bucket: "minio-amphora-bucket"}
	if in.pg, err = pgstore.Open(ctx, dsn); err != nil {
		return nil, err
	}
	in.rdb = redis.NewClient(&redis.Options{Addr: redisAddr})
	if in.obj, err = objstore.New(ctx, "http://"+minioAddr, in.bucket, minioKey, minioSecret); err != nil {
		return nil, err
	}
	return in, nil
}

func getInfra(t *testing.T) *infra {
	t.Helper()
	sysMu.Lock()
	defer sysMu.Unlock()
	if !sysInfraReady {
		sysInfra, sysInfraErr = startInfra()
		sysInfraReady = true
	}
	if sysInfraErr != nil {
		t.Skipf("containers unavailable: %v", sysInfraErr)
	}
	return sysInfra
}

// clearAllData is PersistenceTestEnvironment.clearAllData.
func (in *infra) clearAllData(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	if _, err := in.pg.Pool.Exec(ctx, "TRUNCATE secret_tags, amphora_secrets"); err != nil {
		t.Fatal(err)
	}
	if err := in.rdb.FlushAll(ctx).Err(); err != nil {
		t.Fatal(err)
	}
	for o := range in.minio.ListObjects(ctx, in.bucket, minio.ListObjectsOptions{}) {
		if o.Err != nil {
			t.Fatal(o.Err)
		}
		if err := in.minio.RemoveObject(ctx, in.bucket, o.Key, minio.RemoveObjectOptions{}); err != nil {
			t.Fatal(err)
		}
	}
}

// ---- fake OPA ----

type opaLog struct {
	mu      sync.Mutex
	actions []string
	subject []string
}

// newOPA allows exactly the authorized user (OpaClient.isAllowed(any, any, authorizedUserId, any) -> true).
func newOPA(t *testing.T) (*opa.Client, *opaLog) {
	l := &opaLog{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Input struct{ Subject string } }
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &req)
		l.mu.Lock()
		l.actions = append(l.actions, strings.TrimPrefix(r.URL.Path, "/v1/data/carbynestack/def/"))
		l.subject = append(l.subject, req.Input.Subject)
		l.mu.Unlock()
		fmt.Fprintf(w, `{"result":%v}`, req.Input.Subject == authorizedUserID)
	}))
	t.Cleanup(srv.Close)
	c, err := opa.New(srv.URL, "carbynestack.def", nil)
	if err != nil {
		t.Fatal(err)
	}
	return c, l
}

// ---- the system under test ----

type system struct {
	in  *infra
	srv *httptest.Server
	opa *opaLog
}

func newSystem(t *testing.T) *system {
	in := getInfra(t)
	in.clearAllData(t)
	oc, ol := newOPA(t)
	p, _ := new(big.Int).SetString("198766463529478683931867765928436695041", 10)
	r, _ := new(big.Int).SetString("141515903391459779531506841503331516415", 10)
	ri, _ := new(big.Int).SetString("133854242216446749056083838363708373830", 10)
	sp := spdz.New(p, r, ri)
	svc := &service.Service{PG: in.pg, Obj: in.obj, Cache: rediscache.New(in.rdb, "input-mask-store", "interim-value-store"),
		Opa: oc, Spdz: sp, MacKey: new(big.Int).SetInt64(1), PlayerID: 0}
	api := &API{Svc: svc, JWT: jwt.Reader{Field: "sub"}}
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)
	return &system{in: in, srv: srv, opa: ol}
}

type tagJSON struct {
	Key       string `json:"key"`
	Value     string `json:"value"`
	ValueType string `json:"valueType"`
}

type metaJSON struct {
	SecretID string    `json:"secretId"`
	Tags     []tagJSON `json:"tags"`
}

type pageJSON struct {
	Content       []metaJSON `json:"content"`
	Number        int        `json:"number"`
	Size          int        `json:"size"`
	TotalElements int64      `json:"totalElements"`
	TotalPages    int        `json:"totalPages"`
}

type testShare struct {
	id   string
	data []byte
	tags []tagJSON
}

func (s testShare) body() string {
	tags, _ := json.Marshal(s.tags)
	return fmt.Sprintf(`{"secretId":"%s","tags":%s,"data":"%s"}`, s.id, tags, base64.StdEncoding.EncodeToString(s.data))
}

func shareData(words int, seed byte) []byte {
	b := make([]byte, words*spdz.ShareWidth)
	for i := range b {
		b[i] = byte(i) + seed
	}
	return b
}

func tg(k, v string) tagJSON { return tagJSON{k, v, "STRING"} }

var (
	share1 = testShare{"82a73814-321c-4261-abcd-27c6c0ebfb26", shareData(3, 1), []tagJSON{tg("key1", "value1%,$"), tg("number", "123")}}
	share2 = testShare{"82a73814-321c-4261-abcd-27c6c0ebfb27", shareData(1, 2), []tagJSON{tg("key1", "value2,"), tg("number", "100")}}
	share3 = testShare{"82a73814-321c-4261-abcd-27c6c0ebfb28", shareData(5, 3), []tagJSON{tg("key1", "value1%,$"), tg("number", "112")}}
	share4 = testShare{"82a73814-321c-4261-abcd-27c6c0ebfb29", shareData(2, 4), []tagJSON{tg("empty-value", "")}}
)

func (s *system) do(method, target, body string, hdr map[string]string) (int, string) {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, _ := http.NewRequest(method, s.srv.URL+target, rd)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func (s *system) createTestSecretShares(t *testing.T) {
	t.Helper()
	for _, sh := range []testShare{share1, share2, share3, share4} {
		if code, body := s.do("POST", "/intra-vcp/secret-shares", sh.body(), nil); code != 201 {
			t.Fatalf("upload %s: %d %s", sh.id, code, body)
		}
	}
}

func (s *system) list(t *testing.T, query string) pageJSON {
	t.Helper()
	code, body := s.do("GET", "/secret-shares"+query, "", nil)
	if code != 200 {
		t.Fatalf("list %s: %d %s", query, code, body)
	}
	var p pageJSON
	if err := json.Unmarshal([]byte(body), &p); err != nil {
		t.Fatal(err)
	}
	return p
}

// equalsButIgnoreReservedTags is MetadataMatchers.equalsButIgnoreReservedTags.
func equalsButIgnoreReservedTags(t *testing.T, actual metaJSON, want testShare) {
	t.Helper()
	var got []string
	for _, tag := range actual.Tags {
		if tag.Key != service.CreationDateKey && tag.Key != opa.OwnerTagKey {
			got = append(got, tag.Key+"="+tag.Value+"/"+tag.ValueType)
		}
	}
	var exp []string
	for _, tag := range want.tags {
		exp = append(exp, tag.Key+"="+tag.Value+"/"+tag.ValueType)
	}
	sort.Strings(got)
	sort.Strings(exp)
	if actual.SecretID != want.id || strings.Join(got, "|") != strings.Join(exp, "|") {
		t.Fatalf("got %s %v, want %s %v", actual.SecretID, got, want.id, exp)
	}
}

func bearer() map[string]string { return map[string]string{"Authorization": authHeaderValue} }

func TestSystemPostSecretShares(t *testing.T) { // givenSuccessfulRequest_whenPostSecretShares_thenReturnExpectedContent
	s := newSystem(t)
	code, body := s.do("POST", "/intra-vcp/secret-shares", share1.body(), nil)
	if code != 201 {
		t.Fatal(code, body)
	}
	var uri string
	if err := json.Unmarshal([]byte(body), &uri); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(uri)
	if err != nil || u.Path != "/intra-vcp/secret-shares/"+share1.id {
		t.Fatalf("Response contains not expected content! %s", uri)
	}
	// the data is persisted: the same id cannot be uploaded twice
	if code, _ := s.do("POST", "/intra-vcp/secret-shares", share1.body(), nil); code != 409 {
		t.Fatal(code)
	}
}

func TestSystemListSecrets(t *testing.T) { // givenSuccessfulRequest_whenListSecrets_thenReturnExpectedContent
	s := newSystem(t)
	s.createTestSecretShares(t)
	p := s.list(t, "")
	if p.TotalElements != 4 {
		t.Fatalf("Response contains wrong total number of results! %d", p.TotalElements)
	}
	equalsButIgnoreReservedTags(t, p.Content[0], share1)
	// reserved creation-date tag is added by the service
	found := false
	for _, tag := range p.Content[0].Tags {
		found = found || tag.Key == service.CreationDateKey
	}
	if !found {
		t.Fatal("creation-date tag missing")
	}
}

func TestSystemListEmpty(t *testing.T) {
	// getSecretList(Sort) is PageRequest.of(0, Integer.MAX_VALUE): an empty result has zero pages.
	s := newSystem(t)
	p := s.list(t, "")
	if p.TotalElements != 0 || p.TotalPages != 0 || p.Number != 0 || p.Size != 2147483647 || len(p.Content) != 0 {
		t.Fatalf("%+v", p)
	}
	p = s.list(t, "?pageNumber=0&pageSize=1")
	if p.TotalElements != 0 || p.TotalPages != 0 || p.Number != 0 || p.Size != 1 || len(p.Content) != 0 {
		t.Fatalf("%+v", p)
	}
}

func filterQuery(f string) string { return "?" + url.Values{"filter": {f}}.Encode() }

func TestSystemListSecretsWithFilters(t *testing.T) {
	s := newSystem(t)
	s.createTestSecretShares(t)
	cases := []struct {
		name, filter string
		want         testShare
	}{
		{"NumberComparisonFilter", "number>100,number<120", share3},
		// the filter parts are URL-decoded once more by the service: %2C is a comma inside the value
		{"StringEqualFilter", "key1:value2%2C", share2},
		{"StringAndNumberComparisonFilters", "key1:value1%25%2C%24,number>120", share1},
	}
	for _, c := range cases {
		t.Run("givenSuccessfulRequest_whenListSecretsWith"+c.name, func(t *testing.T) {
			p := s.list(t, filterQuery(c.filter))
			if p.TotalElements != 1 || len(p.Content) != 1 {
				t.Fatalf("Response contains wrong total number of results! %+v", p)
			}
			equalsButIgnoreReservedTags(t, p.Content[0], c.want)
		})
	}
}

func TestSystemListSecretsWithPagination(t *testing.T) {
	s := newSystem(t)
	s.createTestSecretShares(t)
	p := s.list(t, "?pageNumber=1&pageSize=1")
	if p.TotalElements != 4 || len(p.Content) != 1 || p.Number != 1 || p.Size != 1 || p.TotalPages != 4 {
		t.Fatalf("%+v", p)
	}
	equalsButIgnoreReservedTags(t, p.Content[0], share2)
}

func TestSystemDeleteSecret(t *testing.T) { // givenSuccessfulRequest_whenDeleteSecret_thenReturnRemoveAndDoNoLongerReturn
	s := newSystem(t)
	s.createTestSecretShares(t)
	if n := s.list(t, "").TotalElements; n != 4 {
		t.Fatalf("Response contains wrong total number of results prior to deletion! %d", n)
	}
	if code, body := s.do("DELETE", "/secret-shares/"+share1.id, "", bearer()); code/100 != 2 {
		t.Fatalf("Request must be successful. %d %s", code, body)
	}
	if n := s.list(t, "").TotalElements; n != 3 {
		t.Fatalf("Response contains wrong total number of results after deletion! %d", n)
	}
	// the data object is gone as well
	if _, err := s.in.obj.Get(context.Background(), share1.id); err == nil {
		t.Fatal("share data must be removed from the object store")
	}
	if len(s.opa.actions) != 1 || s.opa.actions[0] != "delete" || s.opa.subject[0] != authorizedUserID {
		t.Fatalf("OPA calls: %v %v", s.opa.actions, s.opa.subject)
	}
}

func TestSystemDeleteUnknownSecretDoesNotTouchOthers(t *testing.T) { // givenUnknownSecretId_whenDeleteSecretShare_thenDoNotTouchOtherSecrets
	s := newSystem(t)
	s.createTestSecretShares(t)
	unknown := "82a73814-321c-4261-abcd-27c6c0ebfb20"
	// Java: restTemplate.delete without Authorization header (400, the call is ignored by the test)
	if code, _ := s.do("DELETE", "/secret-shares/"+unknown, "", nil); code != 400 {
		t.Fatal(code)
	}
	if code, body := s.do("DELETE", "/secret-shares/"+unknown, "", bearer()); code != 404 || body != `"No secret with the given id #`+unknown+` exists."` {
		t.Fatal(code, body)
	}
	if n := s.list(t, "").TotalElements; n != 4 {
		t.Fatalf("Response contains wrong total number of results after fake deletion! %d", n)
	}
}

func TestSystemListSecretsWithSorting(t *testing.T) { // givenSuccessfulRequest_whenListSecretsWithNSorting_thenReturnExpectedContent
	s := newSystem(t)
	s.createTestSecretShares(t)
	p := s.list(t, "?sort=key1&dir=DESC")
	if p.TotalElements != 4 || len(p.Content) != 4 {
		t.Fatalf("Response contains wrong total number of results! %+v", p)
	}
	byID := map[string]metaJSON{}
	for _, m := range p.Content {
		byID[m.SecretID] = m
	}
	for _, sh := range []testShare{share4, share2, share1, share3} {
		m, ok := byID[sh.id]
		if !ok {
			t.Fatalf("%s missing", sh.id)
		}
		equalsButIgnoreReservedTags(t, m, sh)
	}
}

// Additional system-level behaviours of the controllers on top of the real stores.
func TestSystemTagLifecycleAndAuthorization(t *testing.T) {
	s := newSystem(t)
	s.createTestSecretShares(t)
	base := "/secret-shares/" + share2.id + "/tags"
	if code, _ := s.do("POST", base, `{"key":"new","value":"1","valueType":"LONG"}`, bearer()); code != 201 {
		t.Fatal(code)
	}
	code, body := s.do("GET", base+"/new", "", bearer())
	if code != 200 || body != `{"key":"new","value":"1","valueType":"LONG"}` {
		t.Fatal(code, body)
	}
	if code, _ := s.do("PUT", base+"/new", `{"key":"new","value":"2","valueType":"LONG"}`, bearer()); code != 200 {
		t.Fatal(code)
	}
	if code, body := s.do("GET", base+"/new", "", bearer()); code != 200 || !strings.Contains(body, `"value":"2"`) {
		t.Fatal(code, body)
	}
	if code, _ := s.do("DELETE", base+"/new", "", bearer()); code != 200 {
		t.Fatal(code)
	}
	if code, _ := s.do("GET", base+"/new", "", bearer()); code != 404 {
		t.Fatal(code)
	}
	// reserved keys cannot be created
	if code, body := s.do("POST", base, `{"key":"owner","value":"x"}`, bearer()); code != 400 || body != `"\"owner\" is a reserved key."` {
		t.Fatal(code, body)
	}
	// an unauthorized user is rejected by the OPA policy
	other := map[string]string{"Authorization": "Bearer h." + base64.StdEncoding.EncodeToString([]byte(`{"sub":"someone-else"}`)) + ".s"}
	if code, body := s.do("GET", base, "", other); code != 401 || body != `"User is not authorized to read tags for this secret"` {
		t.Fatal(code, body)
	}
	if code, body := s.do("DELETE", "/secret-shares/"+share2.id, "", other); code != 401 || body != `"User is not authorized to delete this secret"` {
		t.Fatal(code, body)
	}
	// unknown secret
	if code, _ := s.do("GET", "/secret-shares/82a73814-321c-4261-abcd-27c6c0ebfb20/tags", "", bearer()); code != 404 {
		t.Fatal(code)
	}
}
