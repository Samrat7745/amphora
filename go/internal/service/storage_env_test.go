package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/carbynestack/amphora-go/internal/castor"
	"github.com/carbynestack/amphora-go/internal/model"
	"github.com/carbynestack/amphora-go/internal/objstore"
	"github.com/carbynestack/amphora-go/internal/opa"
	"github.com/carbynestack/amphora-go/internal/pgstore"
	"github.com/carbynestack/amphora-go/internal/rediscache"
	"github.com/carbynestack/amphora-go/internal/testkit"
	"github.com/carbynestack/amphora-go/spdz"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/redis/go-redis/v9"
)

// Test environment for the StorageService ports (StorageServiceTest, StorageIT,
// FilterPagingAndNestedSortingIT, InputMask/ODO caching tests). The Java tests mock the Spring
// repositories, the Minio client, Redis and the OPA service; here the real Postgres/MinIO/Redis
// (plain docker containers) are used and OPA is an httptest server that records every evaluation.

var (
	pgC    = testkit.NewLazy(testkit.StartPostgres)
	redisC = testkit.NewLazy(testkit.StartRedis)
	minioC = testkit.NewLazy(testkit.StartMinio)
)

func TestMain(m *testing.M) {
	code := m.Run()
	pgC.Close()
	redisC.Close()
	minioC.Close()
	os.Exit(code)
}

const (
	authorizedSubject = "afc0117f-c9cd-4d8c-acee-fa1433ca0fdd"
	testSecretID      = "3bcf8308-8f50-4d24-a37b-b0075bb5e779"
	testSecretID2     = "0e7cd962-d98e-4eea-82ae-4641399c9ad7"
	testSecretID3     = "e043fb9b-6124-437a-a59b-ce988c5ce1c5"
	unknownID         = "d6d0f4ff-df28-4c96-b7df-95170320eaee"
)

var (
	testTag  = model.Tag{Key: "TEST_KEY", Value: "TEST_VALUE", ValueType: model.TypeString}
	testTag2 = model.Tag{Key: "SUPER_KEY", Value: "MY#SUPER,VALUE", ValueType: model.TypeString}
	testTag3 = model.Tag{Key: "SUPER_KEY", Value: "OTHER~VALUE", ValueType: model.TypeString}
)

type opaCall struct {
	Subject, Action, Package string
	Tags                     []model.Tag
}

// fakeOpa answers policy evaluations: allowed unless rule says otherwise (default: only
// authorizedSubject is allowed, for every action).
type fakeOpa struct {
	srv  *httptest.Server
	mu   sync.Mutex
	call []opaCall
	rule func(c opaCall) bool
}

func newFakeOpa(t *testing.T) *fakeOpa {
	f := &fakeOpa{rule: func(c opaCall) bool { return c.Subject == authorizedSubject }}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var in struct {
			Input struct {
				Subject string `json:"subject"`
				Tags    []struct {
					Key       string `json:"key"`
					Value     string `json:"value"`
					ValueType string `json:"valueType"`
				} `json:"tags"`
			} `json:"input"`
		}
		_ = json.Unmarshal(body, &in)
		c := opaCall{Subject: in.Input.Subject}
		p := strings.TrimPrefix(r.URL.Path, "/v1/data/")
		i := strings.LastIndex(p, "/")
		c.Package, c.Action = strings.ReplaceAll(p[:i], "/", "."), p[i+1:]
		// the action may be a two segment path (tag/create)
		for _, a := range []string{"tag/create", "tag/read", "tag/update", "tag/delete"} {
			if strings.HasSuffix(p, "/"+a) {
				c.Action, c.Package = a, strings.ReplaceAll(strings.TrimSuffix(p, "/"+a), "/", ".")
			}
		}
		for _, t := range in.Input.Tags {
			c.Tags = append(c.Tags, model.Tag{Key: t.Key, Value: t.Value, ValueType: model.TagValueType(t.ValueType)})
		}
		f.mu.Lock()
		f.call = append(f.call, c)
		rule := f.rule
		f.mu.Unlock()
		_, _ = fmt.Fprintf(w, `{"result":%v}`, rule(c))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeOpa) calls() []opaCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]opaCall(nil), f.call...)
}

// stubCastor serves tuples by type; counts the calls per (type, request id).
type stubCastor struct {
	mu     sync.Mutex
	masks  map[string][]castor.Tuple // request id -> input masks (INPUT_MASK_GFP)
	byType func(requestID, tupleType string, count int64) ([]castor.Tuple, error)
	calls  []string
}

func (c *stubCastor) Download(_ context.Context, requestID, tupleType string, count int64) ([]castor.Tuple, error) {
	c.mu.Lock()
	c.calls = append(c.calls, fmt.Sprintf("%s/%s/%d", tupleType, requestID, count))
	c.mu.Unlock()
	return c.byType(requestID, tupleType, count)
}

var (
	testPrime, _ = new(big.Int).SetString("198766463529478683931867765928436695041", 10)
	testR, _     = new(big.Int).SetString("141515903391459779531506841503331516415", 10)
	testRInv, _  = new(big.Int).SetString("133854242216446749056083838363708373830", 10)
)

type env struct {
	t      *testing.T
	svc    *Service
	pg     *pgstore.Store
	obj    *objstore.Store
	rdb    *redis.Client
	opa    *fakeOpa
	castor *stubCastor
	minio  *minio.Client
	bucket string
	now    int64
}

var envSeq atomic.Int64

func newEnv(t *testing.T) *env {
	t.Helper()
	dsn := pgC.Get(t)
	raddr := redisC.Get(t)
	maddr := minioC.Get(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)

	pg, err := pgstore.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pg.Close)
	// PersistenceTestEnvironment.clearAllData
	if _, err := pg.Pool.Exec(ctx, "TRUNCATE secret_tags, amphora_secrets"); err != nil {
		t.Fatal(err)
	}
	rdb := redis.NewClient(&redis.Options{Addr: raddr})
	t.Cleanup(func() { _ = rdb.Close() })
	if err := rdb.FlushAll(ctx).Err(); err != nil {
		t.Fatal(err)
	}
	bucket := fmt.Sprintf("amphora-test-%d-%d", os.Getpid(), envSeq.Add(1))
	obj, err := objstore.New(ctx, "http://"+maddr, bucket, testkit.MinioAccessKey, testkit.MinioSecretKey)
	if err != nil {
		t.Fatal(err)
	}
	mc, err := minio.New(maddr, &minio.Options{Creds: credentials.NewStaticV4(testkit.MinioAccessKey, testkit.MinioSecretKey, "")})
	if err != nil {
		t.Fatal(err)
	}
	fo := newFakeOpa(t)
	oc, err := opa.New(fo.srv.URL, "default", nil)
	if err != nil {
		t.Fatal(err)
	}
	cache := rediscache.New(rdb, "input-mask-store", "interim-value-store")
	sp := spdz.New(testPrime, testR, testRInv)
	fc := &stubCastor{masks: map[string][]castor.Tuple{}}
	e := &env{t: t, minio: mc, pg: pg, obj: obj, rdb: rdb, opa: fo, castor: fc, bucket: bucket, now: 1_700_000_000_000}
	svc := &Service{
		PG: pg, Obj: obj, Cache: cache, Opa: oc, Castor: fc, Spdz: sp,
		MacKey: big.NewInt(12345), PlayerID: 0,
		Now: func() time.Time { return time.UnixMilli(e.now) },
	}
	svc.ODO = &ODO{Spdz: sp, Castor: fc, Interim: cache, PlayerID: 0, NumPartners: 0, OpeningTimeout: 5 * time.Second}
	e.svc = svc
	return e
}

func (e *env) ctx() context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	e.t.Cleanup(cancel)
	return ctx
}

// persist is persistObjectWithIdAndTags: it writes the rows directly (no reserved tags added).
func (e *env) persist(id string, tags ...model.Tag) {
	e.t.Helper()
	ctx := e.ctx()
	err := e.pg.InTx(ctx, func(q pgstore.Querier) error {
		if err := e.pg.InsertSecret(ctx, q, id); err != nil {
			return err
		}
		for _, tg := range tags {
			if err := e.pg.InsertTag(ctx, q, id, tg); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) dbTags(id string) []model.Tag {
	e.t.Helper()
	rows, err := e.pg.Tags(e.ctx(), e.pg.Pool, id)
	if err != nil {
		e.t.Fatal(err)
	}
	return tagList(rows)
}

func (e *env) exists(id string) bool {
	e.t.Helper()
	ok, err := e.pg.Exists(e.ctx(), e.pg.Pool, id)
	if err != nil {
		e.t.Fatal(err)
	}
	return ok
}

func (e *env) putData(id string, data []byte) {
	e.t.Helper()
	if err := e.obj.Put(e.ctx(), id, data); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) hasData(id string) bool {
	_, err := e.obj.Get(e.ctx(), id)
	return err == nil
}

// breakData makes every Minio operation of the service fail (the bucket vanishes).
func (e *env) breakData() {
	e.t.Helper()
	ctx := e.ctx()
	for o := range e.minio.ListObjects(ctx, e.bucket, minio.ListObjectsOptions{}) {
		if o.Err != nil {
			e.t.Fatal(o.Err)
		}
		if err := e.minio.RemoveObject(ctx, e.bucket, o.Key, minio.RemoveObjectOptions{}); err != nil {
			e.t.Fatal(err)
		}
	}
	if err := e.minio.RemoveBucket(ctx, e.bucket); err != nil {
		e.t.Fatal(err)
	}
}

func wantErr(t *testing.T, err error, kind model.Kind, msg string) {
	t.Helper()
	var me *model.Error
	if !errors.As(err, &me) {
		t.Fatalf("want model.Error(kind=%d, %q), got %T %v", kind, msg, err, err)
	}
	if me.Kind != kind || me.Msg != msg {
		t.Fatalf("want kind=%d msg=%q, got kind=%d msg=%q", kind, msg, me.Kind, me.Msg)
	}
}

func sameTags(a, b []model.Tag) bool {
	if len(a) != len(b) {
		return false
	}
	used := make([]bool, len(b))
outer:
	for _, x := range a {
		for i, y := range b {
			if !used[i] && x == y {
				used[i] = true
				continue outer
			}
		}
		return false
	}
	return true
}

func tagByKey(tags []model.Tag, key string) (model.Tag, bool) {
	for _, t := range tags {
		if t.Key == key {
			return t, true
		}
	}
	return model.Tag{}, false
}

func asModelErr(err error, target **model.Error) bool { return errors.As(err, target) }

func listParams(f []model.TagFilter) pgstore.ListParams { return pgstore.ListParams{Filters: f} }
