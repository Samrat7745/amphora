package rediscache

import (
	"bytes"
	"context"
	"crypto/rand"
	"math/big"
	mrand "math/rand"
	"os"
	"testing"
	"time"

	"github.com/Samrat7745/amphora/go/internal/javaser"
	"github.com/Samrat7745/amphora/go/internal/model"
	"github.com/Samrat7745/amphora/go/internal/testkit"
	"github.com/redis/go-redis/v9"
)

// Ports InputMaskStoreTest, InputMaskStoreRedisIT and InterimValueCachingServiceTest. The Java unit
// tests assert on Mockito interactions with the RedisTemplate (key names, stored objects); here the
// same facts are observed in a real Redis: key names, the Java-serialized values and eviction.

var redisC = testkit.NewLazy(testkit.StartRedis)

func TestMain(m *testing.M) {
	code := m.Run()
	redisC.Close()
	os.Exit(code)
}

const (
	testRequestID = "b380f595-f301-4577-9f44-6d78728c38c3"
	testCacheName = "testCache"
)

func setup(t *testing.T) (*Cache, *redis.Client, context.Context) {
	t.Helper()
	addr := redisC.Get(t)
	rdb := redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() { _ = rdb.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	if err := rdb.FlushAll(ctx).Err(); err != nil {
		t.Fatal(err)
	}
	return New(rdb, testCacheName, testCacheName), rdb, ctx
}

func randomShares(n int) []javaser.Share {
	out := make([]javaser.Share, n)
	for i := range out {
		out[i] = javaser.Share{Value: make([]byte, 16), Mac: make([]byte, 16)}
		_, _ = rand.Read(out[i].Value)
		_, _ = rand.Read(out[i].Mac)
	}
	return out
}

func sameShares(a, b []javaser.Share) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !bytes.Equal(a[i].Value, b[i].Value) || !bytes.Equal(a[i].Mac, b[i].Mac) {
			return false
		}
	}
	return true
}

func TestPing(t *testing.T) {
	c, _, ctx := setup(t)
	if err := c.Ping(ctx); err != nil {
		t.Fatal(err)
	}
}

// givenSuccessfulRequest_whenPutInputMasks_thenStoreInCacheAndReturnList: stored under
// "<store>::<requestId>" as the Java-serialized TupleList.
func TestPutInputMasksKeyAndValue(t *testing.T) {
	c, rdb, ctx := setup(t)
	shares := randomShares(5)
	if err := c.PutInputMasks(ctx, testRequestID, shares); err != nil {
		t.Fatal(err)
	}
	raw, err := rdb.Get(ctx, testCacheName+"::"+testRequestID).Bytes()
	if err != nil {
		t.Fatalf("key %s::%s missing: %v", testCacheName, testRequestID, err)
	}
	if !bytes.Equal(raw, javaser.EncodeInputMaskListFromCastor(shares)) {
		t.Fatal("stored value is not the Java serialization of the input mask list")
	}
	if ttl, _ := rdb.TTL(ctx, testCacheName+"::"+testRequestID).Result(); ttl != -1 {
		t.Fatalf("entries must not expire, ttl=%v", ttl)
	}
	if n, _ := rdb.DBSize(ctx).Result(); n != 1 {
		t.Fatalf("exactly one key expected, got %d", n)
	}
}

// givenInputMasksInCache_whenGetInputMasks_thenReturnList: a value written by the Java service is read.
func TestGetInputMasksReadsJavaValue(t *testing.T) {
	c, rdb, ctx := setup(t)
	java, err := os.ReadFile("../javaser/testdata/inputmask_list3.bin")
	if err != nil {
		t.Fatal(err)
	}
	if err := rdb.Set(ctx, testCacheName+"::"+testRequestID, java, 0).Err(); err != nil {
		t.Fatal(err)
	}
	got, found, err := c.GetInputMasks(ctx, testRequestID)
	if err != nil || !found || len(got) != 3 {
		t.Fatalf("got %v %v %v", got, found, err)
	}
}

// givenInputMasksNotInCache_whenGetInputMasks_thenThrowException / givenNoDataForKeyInCache...
func TestGetInputMasksAbsent(t *testing.T) {
	c, _, ctx := setup(t)
	got, found, err := c.GetInputMasks(ctx, testRequestID)
	if err != nil || found || got != nil {
		t.Fatalf("absent key must be (nil,false,nil), got %v %v %v", got, found, err)
	}
}

func TestGetInputMasksUnreadableValueIsAnError(t *testing.T) {
	c, rdb, ctx := setup(t)
	if err := rdb.Set(ctx, testCacheName+"::"+testRequestID, "garbage", 0).Err(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.GetInputMasks(ctx, testRequestID); err == nil {
		t.Fatal("garbage must fail")
	}
}

// givenMultipleRequestsForSameKey_whenGetInputMasks_thenReturnSameResult
func TestGetInputMasksIsRepeatable(t *testing.T) {
	c, _, ctx := setup(t)
	shares := randomShares(42)
	if err := c.PutInputMasks(ctx, testRequestID, shares); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		got, found, err := c.GetInputMasks(ctx, testRequestID)
		if err != nil || !found || !sameShares(got, shares) {
			t.Fatalf("read %d differs: %v %v", i, found, err)
		}
	}
}

// givenConsecutiveCallsForSameKey_whenFetchAndCacheInputMasks_thenReplaceExistingData
func TestPutInputMasksReplacesExisting(t *testing.T) {
	c, _, ctx := setup(t)
	first, second := randomShares(42), randomShares(42)
	for _, s := range [][]javaser.Share{first, second} {
		if err := c.PutInputMasks(ctx, testRequestID, s); err != nil {
			t.Fatal(err)
		}
	}
	got, _, err := c.GetInputMasks(ctx, testRequestID)
	if err != nil || !sameShares(got, second) || sameShares(got, first) {
		t.Fatal("second put must replace the first", err)
	}
}

// givenSuccessfulRequest_whenRemoveInputMasks_thenDeleteFromCache
func TestDeleteInputMasks(t *testing.T) {
	c, rdb, ctx := setup(t)
	if err := c.PutInputMasks(ctx, testRequestID, randomShares(3)); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteInputMasks(ctx, testRequestID); err != nil {
		t.Fatal(err)
	}
	if n, _ := rdb.Exists(ctx, testCacheName+"::"+testRequestID).Result(); n != 0 {
		t.Fatal("key must be removed")
	}
	if _, found, _ := c.GetInputMasks(ctx, testRequestID); found {
		t.Fatal("must not be found after removal")
	}
	// removing an absent entry is not an error
	if err := c.DeleteInputMasks(ctx, testRequestID); err != nil {
		t.Fatal(err)
	}
}

// the two stores use independent key prefixes
func TestStoresUseSeparatePrefixes(t *testing.T) {
	addr := redisC.Get(t)
	rdb := redis.NewClient(&redis.Options{Addr: addr})
	defer rdb.Close()
	ctx := context.Background()
	_ = rdb.FlushAll(ctx).Err()
	c := New(rdb, "input-mask-store", "interim-value-store")
	if err := c.PutInputMasks(ctx, testRequestID, randomShares(1)); err != nil {
		t.Fatal(err)
	}
	if err := c.PutInterim(ctx, exchange(testRequestID, 1, 1)); err != nil {
		t.Fatal(err)
	}
	keys, _ := rdb.Keys(ctx, "*").Result()
	want := map[string]bool{"input-mask-store::" + testRequestID: true, "interim-value-store::" + testRequestID + "_1": true}
	if len(keys) != 2 || !want[keys[0]] || !want[keys[1]] {
		t.Fatalf("unexpected keys %v", keys)
	}
}

func exchange(op string, player int32, n int) model.ExchangeObject {
	rnd := mrand.New(mrand.NewSource(42))
	e := model.ExchangeObject{OperationID: op, PlayerID: player, Values: []model.FactorPair{}}
	for i := 0; i < n; i++ {
		e.Values = append(e.Values, model.FactorPair{A: big.NewInt(rnd.Int63n(32)), B: big.NewInt(rnd.Int63n(32))})
	}
	return e
}

const opID = "2affd2b3-bee6-4a25-9943-1fd19ff6f3b6"

// givenSuccessfulRequest_whenPuttingInterimValue_thenStoreInCacheWithCorrectKey
func TestPutInterimKeyAndValue(t *testing.T) {
	c, rdb, ctx := setup(t)
	e := exchange(opID, 37, 21)
	if err := c.PutInterim(ctx, e); err != nil {
		t.Fatal(err)
	}
	key := testCacheName + "::" + opID + "_37"
	raw, err := rdb.Get(ctx, key).Bytes()
	if err != nil {
		t.Fatalf("key %s missing: %v", key, err)
	}
	want := javaser.EncodeExchangeObject(javaser.ExchangeObject{OperationID: opID, PlayerID: 37, Values: func() []javaser.FactorPair {
		var o []javaser.FactorPair
		for _, p := range e.Values {
			o = append(o, javaser.FactorPair{A: p.A, B: p.B})
		}
		return o
	}()})
	if !bytes.Equal(raw, want) {
		t.Fatal("stored value is not the Java serialization of the exchange object")
	}
	// putting again overwrites
	e2 := exchange(opID, 37, 3)
	if err := c.PutInterim(ctx, e2); err != nil {
		t.Fatal(err)
	}
	got, err := c.GetAndEvictInterim(ctx, opID, 37)
	if err != nil || len(got) != 3 {
		t.Fatalf("overwrite not effective: %v %v", got, err)
	}
}

// ...thenReturnOptionWithObjectAndEvict
func TestGetAndEvictInterim(t *testing.T) {
	c, rdb, ctx := setup(t)
	e := exchange(opID, 5, 17)
	if err := c.PutInterim(ctx, e); err != nil {
		t.Fatal(err)
	}
	got, err := c.GetAndEvictInterim(ctx, opID, 5)
	if err != nil || len(got) != len(e.Values) {
		t.Fatalf("got %v %v", got, err)
	}
	for i := range got {
		if got[i].A.Cmp(e.Values[i].A) != 0 || got[i].B.Cmp(e.Values[i].B) != 0 {
			t.Fatalf("pair %d differs", i)
		}
	}
	if n, _ := rdb.Exists(ctx, testCacheName+"::"+opID+"_5").Result(); n != 0 {
		t.Fatal("entry must be evicted")
	}
	// a second read finds nothing
	again, err := c.GetAndEvictInterim(ctx, opID, 5)
	if err != nil || again != nil {
		t.Fatalf("second read must be none: %v %v", again, err)
	}
}

// ...thenReturnFailedOptionAndDontCallEvict: absent entries yield none and touch nothing else.
func TestGetAndEvictInterimAbsent(t *testing.T) {
	c, rdb, ctx := setup(t)
	if err := rdb.Set(ctx, "other", "x", 0).Err(); err != nil {
		t.Fatal(err)
	}
	got, err := c.GetAndEvictInterim(ctx, "a7d4970c-36de-4880-b91b-6656dff3bac9", 0)
	if err != nil || got != nil {
		t.Fatalf("got %v %v", got, err)
	}
	if n, _ := rdb.DBSize(ctx).Result(); n != 1 {
		t.Fatal("other keys must be untouched")
	}
}

// Java: Try(...).toOption() -- unreadable values count as absent but the key is evicted.
func TestGetAndEvictInterimUnreadable(t *testing.T) {
	c, rdb, ctx := setup(t)
	key := testCacheName + "::" + opID + "_1"
	if err := rdb.Set(ctx, key, "garbage", 0).Err(); err != nil {
		t.Fatal(err)
	}
	got, err := c.GetAndEvictInterim(ctx, opID, 1)
	if err != nil || got != nil {
		t.Fatalf("got %v %v", got, err)
	}
	if n, _ := rdb.Exists(ctx, key).Result(); n != 0 {
		t.Fatal("unreadable entry must be evicted")
	}
}

func TestInterimKeyForNullOperationID(t *testing.T) {
	c, rdb, ctx := setup(t)
	if err := c.PutInterim(ctx, model.ExchangeObject{PlayerID: 2}); err != nil {
		t.Fatal(err)
	}
	if n, _ := rdb.Exists(ctx, testCacheName+"::null_2").Result(); n != 1 {
		t.Fatal("Java string concatenation of a null UUID gives \"null\"")
	}
}
