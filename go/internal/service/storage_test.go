package service

// Ports of StorageServiceTest (create/store/list/get/use/delete, see storage_tags_test.go for the
// tag operations), StorageIT and InputMask caching tests. See TESTS_B.md for the mapping.

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"math/big"
	"strconv"
	"testing"
	"time"

	"github.com/Samrat7745/amphora/go/internal/castor"
	"github.com/Samrat7745/amphora/go/internal/javaser"
	"github.com/Samrat7745/amphora/go/internal/model"
	"github.com/Samrat7745/amphora/go/internal/opa"
	"github.com/Samrat7745/amphora/go/internal/pgstore"
	"github.com/Samrat7745/amphora/go/spdz"
)

func randBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}

func randField(t *testing.T) *big.Int {
	t.Helper()
	v, err := rand.Int(rand.Reader, testPrime)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// maskedInput builds a MaskedInput with n random 16 byte values.
func maskedInput(t *testing.T, id string, n int, tags ...model.Tag) *model.MaskedInput {
	t.Helper()
	sp := spdz.New(testPrime, testR, testRInv)
	mi := &model.MaskedInput{SecretID: id, HasData: true, Tags: tags}
	for i := 0; i < n; i++ {
		b, err := sp.ToGfp(randField(t))
		if err != nil {
			t.Fatal(err)
		}
		mi.Data = append(mi.Data, b)
	}
	return mi
}

func (e *env) cacheMasks(id string, n int) []javaser.Share {
	e.t.Helper()
	sp := e.svc.Spdz
	shares := make([]javaser.Share, n)
	for i := range shares {
		v, err1 := sp.ToGfp(randField(e.t))
		m, err2 := sp.ToGfp(randField(e.t))
		if err1 != nil || err2 != nil {
			e.t.Fatal(err1, err2)
		}
		shares[i] = javaser.Share{Value: v, Mac: m}
	}
	if err := e.svc.Cache.PutInputMasks(e.ctx(), id, shares); err != nil {
		e.t.Fatal(err)
	}
	return shares
}

// expectedShare recomputes SecretShareUtil.convertToSecretShare for player 0.
func (e *env) expectedShare(t *testing.T, mi *model.MaskedInput, masks []javaser.Share) []byte {
	t.Helper()
	sp := e.svc.Spdz
	var out []byte
	for i, d := range mi.Data {
		pub, _ := sp.FromGfp(d)
		mv, _ := sp.FromGfp(masks[i].Value)
		mm, _ := sp.FromGfp(masks[i].Mac)
		val := new(big.Int).Mod(new(big.Int).Add(mv, pub), testPrime)
		mac := new(big.Int).Mod(new(big.Int).Add(new(big.Int).Mul(e.svc.MacKey, pub), mm), testPrime)
		vb, _ := sp.ToGfp(val)
		mb, _ := sp.ToGfp(mac)
		out = append(out, vb...)
		out = append(out, mb...)
	}
	return out
}

func creationDate(t *testing.T, tags []model.Tag) int64 {
	t.Helper()
	tg, ok := tagByKey(tags, CreationDateKey)
	if !ok {
		t.Fatalf("tag %q must be given: %v", CreationDateKey, tags)
	}
	v, err := strconv.ParseInt(tg.Value, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// ---- createSecret ----

// givenIdIsAlreadyInUse_whenCreateSecret_thenThrowAlreadyExistsException
func TestCreateSecretIDAlreadyInUse(t *testing.T) {
	e := newEnv(t)
	e.persist(testSecretID)
	mi := maskedInput(t, testSecretID, 1, testTag)
	_, err := e.svc.CreateSecret(e.ctx(), mi, authorizedSubject)
	wantErr(t, err, model.KindAlreadyExists, SecretWithIDExistsMsg)
}

// givenMaskedInputHasTagsWithSameKey_whenCreateObject_thenThrowIllegalArgumentException
func TestCreateSecretDuplicateTagKeys(t *testing.T) {
	e := newEnv(t)
	dup := model.Tag{Key: testTag.Key, Value: "duplicate", ValueType: model.TypeString}
	mi := maskedInput(t, testSecretID, 1, testTag, dup)
	_, err := e.svc.CreateSecret(e.ctx(), mi, authorizedSubject)
	wantErr(t, err, model.KindBadRequest, TagsWithSameKeyMsg)
	if e.exists(testSecretID) {
		t.Fatal("nothing must be persisted")
	}
}

// givenMaskedInputWithReservedKey_whenCreateObject_thenReplaceReservedKey and
// givenSuccessfulRequest_whenCreateObject_thenReturnSecretId
func TestCreateSecretPersistsSecretTagsDataAndRemovesMasks(t *testing.T) {
	reservedInput := model.Tag{Key: CreationDateKey, Value: "MY#SUPER,VALUE", ValueType: model.TypeString}
	ownerInput := model.Tag{Key: opa.OwnerTagKey, Value: "someone-else", ValueType: model.TypeString}
	for _, tc := range []struct {
		name string
		tags []model.Tag
	}{
		{"plain tag", []model.Tag{testTag}},
		{"creation-date supplied by the caller is replaced", []model.Tag{testTag, reservedInput}},
		{"owner supplied by the caller is replaced", []model.Tag{testTag, ownerInput}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.svc.Now = nil // wall clock, compared against a range like CreationDateTagMatchers
			masks := e.cacheMasks(testSecretID, 3)
			mi := maskedInput(t, testSecretID, 3, tc.tags...)
			start := time.Now().UnixMilli()
			id, err := e.svc.CreateSecret(e.ctx(), mi, authorizedSubject)
			end := time.Now().UnixMilli()
			if err != nil || id != testSecretID {
				t.Fatalf("id=%q err=%v", id, err)
			}
			tags := e.dbTags(testSecretID)
			if len(tags) != 3 { // test tag, creation date and owner
				t.Fatalf("want 3 tags (test, creation-date, owner), got %v", tags)
			}
			if tg, ok := tagByKey(tags, testTag.Key); !ok || tg != testTag {
				t.Fatalf("test tag missing: %v", tags)
			}
			if cd := creationDate(t, tags); cd < start || cd > end {
				t.Fatalf("creation-date %d not within [%d,%d]", cd, start, end)
			}
			if tg, _ := tagByKey(tags, CreationDateKey); tg == reservedInput || tg.ValueType != model.TypeString {
				t.Fatalf("reserved creation-date tag was not replaced: %v", tg)
			}
			if tg, ok := tagByKey(tags, opa.OwnerTagKey); !ok || tg.Value != authorizedSubject || tg.ValueType != model.TypeString {
				t.Fatalf("owner must be the authenticated user: %v", tg)
			}
			// data: value||mac words, as computed from masks and the masked input
			got, err := e.obj.Get(e.ctx(), testSecretID)
			if err != nil || !bytes.Equal(got, e.expectedShare(t, mi, masks)) {
				t.Fatalf("stored share differs: %v", err)
			}
			// the used input masks are removed from the cache
			if _, found, _ := e.svc.Cache.GetInputMasks(e.ctx(), testSecretID); found {
				t.Fatal("input masks must be removed after storing")
			}
		})
	}
}

func TestCreateSecretCreationDateUsesCurrentTime(t *testing.T) {
	e := newEnv(t)
	e.cacheMasks(testSecretID, 1)
	e.now = 1_234_567_890_123
	if _, err := e.svc.CreateSecret(e.ctx(), maskedInput(t, testSecretID, 1), authorizedSubject); err != nil {
		t.Fatal(err)
	}
	if cd := creationDate(t, e.dbTags(testSecretID)); cd != e.now {
		t.Fatalf("creation-date=%d", cd)
	}
}

func TestCreateSecretWithoutCachedInputMasks(t *testing.T) {
	e := newEnv(t)
	_, err := e.svc.CreateSecret(e.ctx(), maskedInput(t, testSecretID, 1), authorizedSubject)
	wantErr(t, err, model.KindInternal, fmt.Sprintf(NoInputMasksMsg, testSecretID))
	if e.exists(testSecretID) {
		t.Fatal("nothing must be persisted")
	}
}

func TestCreateSecretMoreDataThanMasks(t *testing.T) {
	e := newEnv(t)
	e.cacheMasks(testSecretID, 1)
	_, err := e.svc.CreateSecret(e.ctx(), maskedInput(t, testSecretID, 2), authorizedSubject)
	wantErr(t, err, model.KindBadRequest, TooFewInputMasksMsg)
	if _, found, _ := e.svc.Cache.GetInputMasks(e.ctx(), testSecretID); !found {
		t.Fatal("masks must stay cached after a failed request")
	}
}

// Storing the data is part of the transaction (Java @Transactional): when Minio fails the metadata
// is rolled back and the input masks are kept.
func TestCreateSecretDataStoreFailureRollsBack(t *testing.T) {
	e := newEnv(t)
	e.cacheMasks(testSecretID, 1)
	e.breakData()
	_, err := e.svc.CreateSecret(e.ctx(), maskedInput(t, testSecretID, 1, testTag), authorizedSubject)
	var me *model.Error
	if err == nil || !asModelErr(err, &me) || me.Kind != model.KindInternal {
		t.Fatalf("want internal error, got %v", err)
	}
	if e.exists(testSecretID) || len(e.dbTags(testSecretID)) != 0 {
		t.Fatal("secret and tags must be rolled back")
	}
	if _, found, _ := e.svc.Cache.GetInputMasks(e.ctx(), testSecretID); !found {
		t.Fatal("masks must not be removed when storing fails")
	}
}

// ---- storeSecretShare ----

func TestStoreSecretShareIDAlreadyInUse(t *testing.T) {
	e := newEnv(t)
	e.persist(testSecretID)
	_, err := e.svc.StoreSecretShare(e.ctx(), &model.SecretShare{Metadata: model.Metadata{SecretID: testSecretID, Tags: []model.Tag{}}, Data: randBytes(32)})
	wantErr(t, err, model.KindAlreadyExists, SecretWithIDExistsMsg)
}

func TestStoreSecretShareDuplicateTagKeys(t *testing.T) {
	e := newEnv(t)
	dup := model.Tag{Key: testTag.Key, Value: "duplicate", ValueType: model.TypeString}
	_, err := e.svc.StoreSecretShare(e.ctx(), &model.SecretShare{Metadata: model.Metadata{SecretID: testSecretID, Tags: []model.Tag{testTag, dup}}, Data: randBytes(32)})
	wantErr(t, err, model.KindBadRequest, TagsWithSameKeyMsg)
}

// givenSecretShareWithReservedKey_whenStoreSecretShare_thenReplaceReservedKey and ..._thenReturnSecretId
func TestStoreSecretShareReservedTags(t *testing.T) {
	reserved := model.Tag{Key: CreationDateKey, Value: "MY#SUPER,VALUE", ValueType: model.TypeString}
	owner := model.Tag{Key: opa.OwnerTagKey, Value: "the-owner", ValueType: model.TypeString}
	e := newEnv(t)
	e.now = 1_700_000_123_456
	data := randBytes(64)
	// a creation-date tag is replaced; an owner tag is kept (shares coming from another player)
	id, err := e.svc.StoreSecretShare(e.ctx(), &model.SecretShare{Metadata: model.Metadata{SecretID: testSecretID, Tags: []model.Tag{testTag, reserved, owner}}, Data: data})
	if err != nil || id != testSecretID {
		t.Fatal(id, err)
	}
	want := []model.Tag{testTag, {Key: CreationDateKey, Value: "1700000123456", ValueType: model.TypeString}, owner}
	if got := e.dbTags(testSecretID); !sameTags(got, want) {
		t.Fatalf("tags %v, want %v", got, want)
	}
	if got, err := e.obj.Get(e.ctx(), testSecretID); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("data not stored: %v", err)
	}
	// without owner tag: test tag and creation date only
	id2, err := e.svc.StoreSecretShare(e.ctx(), &model.SecretShare{Metadata: model.Metadata{SecretID: testSecretID2, Tags: []model.Tag{testTag, reserved}}, Data: data})
	if err != nil || id2 != testSecretID2 {
		t.Fatal(id2, err)
	}
	tags := e.dbTags(testSecretID2)
	if len(tags) != 2 || creationDate(t, tags) != e.now {
		t.Fatalf("want test tag and fresh creation date, got %v", tags)
	}
}

func TestStoreSecretShareWithoutTagsIsRejected(t *testing.T) {
	// SecretShare.getTags() == null: NullPointerException in the Java service
	e := newEnv(t)
	_, err := e.svc.StoreSecretShare(e.ctx(), &model.SecretShare{Metadata: model.Metadata{SecretID: testSecretID}, Data: randBytes(32)})
	var me *model.Error
	if !asModelErr(err, &me) || !me.NullMsg {
		t.Fatalf("want NPE, got %v", err)
	}
	if e.exists(testSecretID) {
		t.Fatal("must not persist")
	}
}

func TestStoreSecretShareDataStoreFailureRollsBack(t *testing.T) {
	e := newEnv(t)
	e.breakData()
	_, err := e.svc.StoreSecretShare(e.ctx(), &model.SecretShare{Metadata: model.Metadata{SecretID: testSecretID, Tags: []model.Tag{testTag}}, Data: randBytes(32)})
	if err == nil {
		t.Fatal("must fail")
	}
	if e.exists(testSecretID) {
		t.Fatal("metadata must be rolled back")
	}
}

// ---- getSecretList ----

func seedTwo(e *env) []model.Metadata {
	e.persist(testSecretID, testTag)
	e.persist(testSecretID2, testTag2)
	return []model.Metadata{{SecretID: testSecretID, Tags: []model.Tag{testTag}}, {SecretID: testSecretID2, Tags: []model.Tag{testTag2}}}
}

func sameMetadata(t *testing.T, got, want []model.Metadata) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d items %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range got {
		if got[i].SecretID != want[i].SecretID || !sameTags(got[i].Tags, want[i].Tags) {
			t.Fatalf("item %d: got %v want %v", i, got[i], want[i])
		}
	}
}

func idsOf(p model.MetadataPage) []string {
	var ids []string
	for _, m := range p.Content {
		ids = append(ids, m.SecretID)
	}
	return ids
}

// givenSuccessfulRequest_whenGetObjectListWithPageable_thenReturnExpectedContent
func TestListUnpaged(t *testing.T) {
	e := newEnv(t)
	want := seedTwo(e)
	p, err := e.svc.List(e.ctx(), pgstore.ListParams{})
	if err != nil {
		t.Fatal(err)
	}
	sameMetadata(t, p.Content, want)
	if p.TotalElements != 2 || p.TotalPages != 1 {
		t.Fatalf("page %+v", p)
	}
}

// ...WithSortConfig / WithFilterAndPageable / WithFilterAndSortConfig: result passes through
// unchanged; the sort configuration is applied on one page of Integer.MAX_VALUE items.
func TestListWithSortAndFilter(t *testing.T) {
	e := newEnv(t)
	seedTwo(e)
	// sort by a key that no secret has behaves like unsorted
	p, err := e.svc.List(e.ctx(), pgstore.ListParams{SortKey: "SortProperty"})
	if err != nil || len(p.Content) != 2 || p.Size != 2147483647 || p.Number != 0 {
		t.Fatalf("%+v %v", p, err)
	}
	// filter
	p, err = e.svc.List(e.ctx(), pgstore.ListParams{Filters: []model.TagFilter{{Key: testTag.Key, Value: testTag.Value, Op: model.OpEquals}}})
	if err != nil {
		t.Fatal(err)
	}
	sameMetadata(t, p.Content, []model.Metadata{{SecretID: testSecretID, Tags: []model.Tag{testTag}}})
	p, err = e.svc.List(e.ctx(), pgstore.ListParams{Filters: []model.TagFilter{{Key: testTag.Key, Value: testTag.Value, Op: model.OpEquals}}, SortKey: "SortProperty", SortDesc: true})
	if err != nil || len(p.Content) != 1 {
		t.Fatalf("%+v %v", p, err)
	}
}

func TestListPaging(t *testing.T) {
	e := newEnv(t)
	for i := 1; i <= 5; i++ {
		e.persist(model.UUIDFromInt(int64(i)), model.Tag{Key: "n", Value: strconv.Itoa(i), ValueType: model.TypeLong})
	}
	for _, tc := range []struct {
		page, size int
		wantIDs    []int64
		total      int64
		pages      int32
	}{
		{0, 2, []int64{1, 2}, 5, 3},
		{1, 2, []int64{3, 4}, 5, 3},
		{2, 2, []int64{5}, 5, 3},
		{3, 2, nil, 5, 3},
		{0, 10, []int64{1, 2, 3, 4, 5}, 5, 1},
	} {
		p, err := e.svc.List(e.ctx(), pgstore.ListParams{Paged: true, Page: tc.page, PageSize: tc.size, SortKey: "n"})
		if err != nil {
			t.Fatal(err)
		}
		var want []string
		for _, i := range tc.wantIDs {
			want = append(want, model.UUIDFromInt(i))
		}
		got := idsOf(p)
		if fmt.Sprint(got) != fmt.Sprint(want) || p.TotalElements != tc.total || p.TotalPages != tc.pages || int(p.Number) != tc.page || int(p.Size) != tc.size {
			t.Fatalf("page %d size %d: got %v %+v", tc.page, tc.size, got, p)
		}
	}
}

// ---- getSecretShare / useSecretShare ----

type shareFn func(e *env, id, subject string) (*model.SecretShare, error)

var shareOps = map[string]struct {
	call   shareFn
	action string
	denied string
}{
	"get": {func(e *env, id, s string) (*model.SecretShare, error) { return e.svc.GetSecretShare(e.ctx(), id, s) }, opa.Read, "User is not authorized to read this secret"},
	"use": {func(e *env, id, s string) (*model.SecretShare, error) { return e.svc.UseSecretShare(e.ctx(), id, s) }, opa.Use, "Requesting program is not authorized to access the secret"},
}

func TestGetAndUseSecretShare(t *testing.T) {
	for name, op := range shareOps {
		t.Run(name+"/not found", func(t *testing.T) {
			e := newEnv(t)
			_, err := op.call(e, testSecretID, authorizedSubject)
			wantErr(t, err, model.KindNotFound, fmt.Sprintf(NoSecretWithIDMsg, testSecretID))
			if len(e.opa.calls()) != 0 {
				t.Fatal("no policy evaluation for unknown secrets")
			}
		})
		t.Run(name+"/subject has no access", func(t *testing.T) {
			e := newEnv(t)
			e.persist(testSecretID)
			e.putData(testSecretID, randBytes(32))
			_, err := op.call(e, testSecretID, "someone-else")
			wantErr(t, err, model.KindUnauthorized, op.denied)
		})
		t.Run(name+"/data cannot be retrieved", func(t *testing.T) {
			e := newEnv(t)
			e.persist(testSecretID, testTag)
			_, err := op.call(e, testSecretID, authorizedSubject)
			wantErr(t, err, model.KindInternal, fmt.Sprintf(GetDataMsg, testSecretID, "The specified key does not exist."))
		})
		t.Run(name+"/success", func(t *testing.T) {
			e := newEnv(t)
			e.persist(testSecretID, testTag)
			data := randBytes(32 * 5)
			e.putData(testSecretID, data)
			s, err := op.call(e, testSecretID, authorizedSubject)
			if err != nil {
				t.Fatal(err)
			}
			if s.SecretID != testSecretID || !sameTags(s.Tags, []model.Tag{testTag}) || !bytes.Equal(s.Data, data) {
				t.Fatalf("unexpected share %+v", s)
			}
			// the policy is evaluated for the subject with the tags of the secret
			calls := e.opa.calls()
			if len(calls) != 1 || calls[0].Subject != authorizedSubject || calls[0].Action != op.action || !sameTags(calls[0].Tags, []model.Tag{testTag}) || calls[0].Package != "default" {
				t.Fatalf("policy calls %+v", calls)
			}
		})
	}
}

// StorageIT: givenObjectWithoutTagsDefined_whenRetrieveSecretShare_thenReturnEmptyListForTags
func TestGetSecretShareWithoutTagsReturnsEmptyList(t *testing.T) {
	e := newEnv(t)
	e.persist(testSecretID)
	e.putData(testSecretID, nil)
	s, err := e.svc.GetSecretShare(e.ctx(), testSecretID, authorizedSubject)
	if err != nil || s.Tags == nil || len(s.Tags) != 0 {
		t.Fatalf("want empty non-nil tags, got %+v %v", s, err)
	}
}

// StorageIT: givenObjectHasNoDataPersisted_whenGetSecretShare_thenThrowAmphoraServiceException
func TestGetSecretShareWithoutDataPersisted(t *testing.T) {
	e := newEnv(t)
	e.persist(testSecretID)
	_, err := e.svc.GetSecretShare(e.ctx(), testSecretID, authorizedSubject)
	wantErr(t, err, model.KindInternal, fmt.Sprintf(GetDataMsg, testSecretID, "The specified key does not exist."))
}

// The policy package comes from the accessPolicy tag of the secret.
func TestPolicyPackageFromTag(t *testing.T) {
	e := newEnv(t)
	e.persist(testSecretID, model.Tag{Key: "accessPolicy", Value: "my.pkg", ValueType: model.TypeString})
	e.putData(testSecretID, nil)
	if _, err := e.svc.GetSecretShare(e.ctx(), testSecretID, authorizedSubject); err != nil {
		t.Fatal(err)
	}
	if c := e.opa.calls(); len(c) != 1 || c[0].Package != "my.pkg" || c[0].Action != "read" {
		t.Fatalf("%+v", c)
	}
}

// ---- deleteSecret ----

func TestDeleteSecret(t *testing.T) {
	t.Run("not found", func(t *testing.T) {
		e := newEnv(t)
		err := e.svc.DeleteSecret(e.ctx(), testSecretID, authorizedSubject)
		wantErr(t, err, model.KindNotFound, fmt.Sprintf(NoSecretWithIDMsg, testSecretID))
	})
	t.Run("subject has no access", func(t *testing.T) {
		e := newEnv(t)
		e.persist(testSecretID, testTag)
		e.putData(testSecretID, randBytes(32))
		err := e.svc.DeleteSecret(e.ctx(), testSecretID, "someone-else")
		wantErr(t, err, model.KindUnauthorized, "User is not authorized to delete this secret")
		if !e.exists(testSecretID) || !e.hasData(testSecretID) {
			t.Fatal("nothing may be deleted")
		}
	})
	t.Run("deleting the data fails and rolls back", func(t *testing.T) {
		e := newEnv(t)
		e.persist(testSecretID, testTag) // no data in Minio
		err := e.svc.DeleteSecret(e.ctx(), testSecretID, authorizedSubject)
		wantErr(t, err, model.KindInternal, fmt.Sprintf(GetDataMsg, testSecretID, "The specified key does not exist."))
		if !e.exists(testSecretID) || len(e.dbTags(testSecretID)) != 1 {
			t.Fatal("the metadata deletion must be rolled back")
		}
	})
	t.Run("success deletes metadata, tags and data", func(t *testing.T) {
		e := newEnv(t)
		e.persist(testSecretID, testTag, testTag2)
		e.persist(testSecretID2, testTag)
		e.putData(testSecretID, randBytes(32))
		e.putData(testSecretID2, randBytes(32))
		if err := e.svc.DeleteSecret(e.ctx(), testSecretID, authorizedSubject); err != nil {
			t.Fatal(err)
		}
		if e.exists(testSecretID) || len(e.dbTags(testSecretID)) != 0 || e.hasData(testSecretID) {
			t.Fatal("secret must be gone")
		}
		if !e.exists(testSecretID2) || len(e.dbTags(testSecretID2)) != 1 || !e.hasData(testSecretID2) {
			t.Fatal("other secrets must stay untouched")
		}
		if c := e.opa.calls(); len(c) != 1 || c[0].Action != opa.Delete || len(c[0].Tags) != 2 {
			t.Fatalf("policy calls %+v", c)
		}
	})
}

// ---- input masks (InputMaskStoreTest / InputMaskStoreRedisIT) ----

func (e *env) castorServes(masks map[string][]castor.Tuple) {
	e.castor.byType = func(req, typ string, count int64) ([]castor.Tuple, error) {
		switch typ {
		case castor.InputMaskGfp:
			if m, ok := masks[req]; ok {
				return m, nil
			}
			// ODO input masks (two per element)
			out := make([]castor.Tuple, count)
			for i := range out {
				out[i] = castor.Tuple{{Value: randGfp(e), Mac: randGfp(e)}}
			}
			return out, nil
		case castor.MultTripleGfp:
			out := make([]castor.Tuple, count)
			for i := range out {
				out[i] = castor.Tuple{{Value: randGfp(e), Mac: randGfp(e)}, {Value: randGfp(e), Mac: randGfp(e)}, {Value: randGfp(e), Mac: randGfp(e)}}
			}
			return out, nil
		}
		return nil, fmt.Errorf("unexpected tuple type %s", typ)
	}
}

func randGfp(e *env) []byte {
	b, err := e.svc.Spdz.ToGfp(randField(e.t))
	if err != nil {
		e.t.Fatal(err)
	}
	return b
}

func randomMaskTuples(e *env, n int) []castor.Tuple {
	out := make([]castor.Tuple, n)
	for i := range out {
		out[i] = castor.Tuple{{Value: randGfp(e), Mac: randGfp(e)}}
	}
	return out
}

// givenSuccessfulRequest_whenPutInputMasks_thenStoreInCacheAndReturnList
func TestInputMasksStoredInCacheAndODOReturned(t *testing.T) {
	e := newEnv(t)
	tuples := randomMaskTuples(e, 5)
	e.castorServes(map[string][]castor.Tuple{testSecretID: tuples})
	odo, err := e.svc.InputMasks(e.ctx(), testSecretID, 5)
	if err != nil {
		t.Fatal(err)
	}
	// ODO secret shares are the value halves of the downloaded masks
	var vals []byte
	for _, tp := range tuples {
		vals = append(vals, tp[0].Value...)
	}
	if !bytes.Equal(odo.SecretShares, vals) || len(odo.RShares) != 5*16 || len(odo.WShares) != 5*16 {
		t.Fatal("unexpected ODO")
	}
	got, found, err := e.svc.Cache.GetInputMasks(e.ctx(), testSecretID)
	if err != nil || !found || len(got) != 5 {
		t.Fatalf("masks not cached: %v %v", found, err)
	}
	for i := range got {
		if !bytes.Equal(got[i].Value, tuples[i][0].Value) || !bytes.Equal(got[i].Mac, tuples[i][0].Mac) {
			t.Fatalf("mask %d differs", i)
		}
	}
	// Castor is asked for exactly the requested count under the request id
	if e.castor.calls[0] != fmt.Sprintf("INPUT_MASK_GFP/%s/5", testSecretID) {
		t.Fatalf("castor calls %v", e.castor.calls)
	}
	// the ODO is computed under the derived request id
	odoReq := spdz.NameUUIDFromBytes([]byte(testSecretID + "_odo-computation"))
	if e.castor.calls[1] != fmt.Sprintf("INPUT_MASK_GFP/%s/10", odoReq) {
		t.Fatalf("castor calls %v", e.castor.calls)
	}
	// stored under the Java key name
	if n, _ := e.rdb.Exists(e.ctx(), "input-mask-store::"+testSecretID).Result(); n != 1 {
		t.Fatal("key input-mask-store::<id> missing")
	}
}

// givenConsecutiveCallsForSameKey_whenFetchAndCacheInputMasks_thenReplaceExistingData
func TestInputMasksConsecutiveCallsReplaceCache(t *testing.T) {
	e := newEnv(t)
	first, second := randomMaskTuples(e, 4), randomMaskTuples(e, 4)
	calls := 0
	e.castorServes(nil)
	base := e.castor.byType
	e.castor.byType = func(req, typ string, count int64) ([]castor.Tuple, error) {
		if typ == castor.InputMaskGfp && req == testSecretID {
			calls++
			if calls == 1 {
				return first, nil
			}
			return second, nil
		}
		return base(req, typ, count)
	}
	for i := 0; i < 2; i++ {
		if _, err := e.svc.InputMasks(e.ctx(), testSecretID, 4); err != nil {
			t.Fatal(err)
		}
	}
	got, _, err := e.svc.Cache.GetInputMasks(e.ctx(), testSecretID)
	if err != nil || len(got) != 4 {
		t.Fatal(err)
	}
	for i := range got {
		if !bytes.Equal(got[i].Value, second[i][0].Value) || bytes.Equal(got[i].Value, first[i][0].Value) {
			t.Fatalf("cached mask %d was not replaced", i)
		}
	}
	// repeated reads return the same result
	again, _, _ := e.svc.Cache.GetInputMasks(e.ctx(), testSecretID)
	for i := range got {
		if !bytes.Equal(got[i].Value, again[i].Value) {
			t.Fatal("cache read is not stable")
		}
	}
}

func TestInputMasksCastorFailure(t *testing.T) {
	e := newEnv(t)
	e.castor.byType = func(string, string, int64) ([]castor.Tuple, error) { return nil, fmt.Errorf("castor is down") }
	_, err := e.svc.InputMasks(e.ctx(), testSecretID, 3)
	wantErr(t, err, model.KindInternal, "castor is down")
	if _, found, _ := e.svc.Cache.GetInputMasks(e.ctx(), testSecretID); found {
		t.Fatal("nothing may be cached")
	}
}

// ---- StorageIT misc ----

// givenAnUnknownId_whenStoringATag_thenThrow and similar not-found checks are in storage_tags_test.go.

func TestCreateSecretThenReadBack(t *testing.T) {
	e := newEnv(t)
	masks := e.cacheMasks(testSecretID, 2)
	mi := maskedInput(t, testSecretID, 2, testTag)
	if _, err := e.svc.CreateSecret(e.ctx(), mi, authorizedSubject); err != nil {
		t.Fatal(err)
	}
	s, err := e.svc.GetSecretShare(e.ctx(), testSecretID, authorizedSubject)
	if err != nil || !bytes.Equal(s.Data, e.expectedShare(t, mi, masks)) || len(s.Tags) != 3 {
		t.Fatalf("%+v %v", s, err)
	}
	// owner tag is what OPA policies see
	if c := e.opa.calls(); len(c) != 1 {
		t.Fatalf("%+v", c)
	} else if o, ok := tagByKey(c[0].Tags, opa.OwnerTagKey); !ok || o.Value != authorizedSubject {
		t.Fatalf("policy input lacks the owner tag: %+v", c[0].Tags)
	}
}
