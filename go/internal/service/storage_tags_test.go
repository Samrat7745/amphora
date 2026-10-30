package service

// Ports of the tag related StorageServiceTest and StorageIT tests.

import (
	"fmt"
	"strings"
	"testing"

	"github.com/carbynestack/amphora-go/internal/model"
	"github.com/carbynestack/amphora-go/internal/opa"
)

var (
	reservedCreationDate = model.Tag{Key: CreationDateKey, Value: "MY#SUPER,VALUE", ValueType: model.TypeString}
	reservedOwner        = model.Tag{Key: opa.OwnerTagKey, Value: "MY#SUPER,VALUE", ValueType: model.TypeString}
)

func notFoundMsg(id string) string { return fmt.Sprintf(NoSecretWithIDMsg, id) }

// ---- storeTag ----

func TestStoreTagReservedKey(t *testing.T) {
	for _, r := range []model.Tag{reservedCreationDate, reservedOwner} {
		e := newEnv(t)
		e.persist(testSecretID)
		_, err := e.svc.StoreTag(e.ctx(), testSecretID, r, authorizedSubject)
		wantErr(t, err, model.KindBadRequest, fmt.Sprintf(IsReservedKeyMsg, r.Key))
		if len(e.opa.calls()) != 0 || len(e.dbTags(testSecretID)) != 0 {
			t.Fatal("the reserved key check comes first and nothing is persisted")
		}
	}
	// the check comes before the lookup of the secret
	e := newEnv(t)
	_, err := e.svc.StoreTag(e.ctx(), unknownID, reservedCreationDate, authorizedSubject)
	wantErr(t, err, model.KindBadRequest, fmt.Sprintf(IsReservedKeyMsg, CreationDateKey))
}

// givenNoSecretShareWithGivenIdInDatabase_whenStoreTag / StorageIT givenSecretIdUnknown_whenStoreTag
func TestStoreTagUnknownSecret(t *testing.T) {
	e := newEnv(t)
	_, err := e.svc.StoreTag(e.ctx(), unknownID, testTag, authorizedSubject)
	wantErr(t, err, model.KindNotFound, notFoundMsg(unknownID))
}

func TestStoreTagAlreadyExists(t *testing.T) {
	e := newEnv(t)
	e.persist(testSecretID, testTag)
	_, err := e.svc.StoreTag(e.ctx(), testSecretID, model.Tag{Key: testTag.Key, Value: "other", ValueType: model.TypeString}, authorizedSubject)
	wantErr(t, err, model.KindAlreadyExists, fmt.Sprintf(TagWithKeyExistsMsg, testTag.Key, testSecretID))
	if got := e.dbTags(testSecretID); len(got) != 1 || got[0] != testTag {
		t.Fatalf("existing tag must be unchanged: %v", got)
	}
}

func TestStoreTagNoAccess(t *testing.T) {
	e := newEnv(t)
	e.persist(testSecretID)
	_, err := e.svc.StoreTag(e.ctx(), testSecretID, testTag, "someone-else")
	wantErr(t, err, model.KindUnauthorized, "User is not authorized to create tags for this secret")
	if len(e.dbTags(testSecretID)) != 0 {
		t.Fatal("nothing may be persisted")
	}
	if c := e.opa.calls(); len(c) != 1 || c[0].Action != opa.TagCreate || c[0].Subject != "someone-else" {
		t.Fatalf("%+v", c)
	}
}

// givenSuccessfulRequest_whenStoreTag_thenPersistTag and the StorageIT store/retrieve tests
func TestStoreTagPersistsAndReturnsKey(t *testing.T) {
	e := newEnv(t)
	e.persist(testSecretID)
	long := model.Tag{Key: "n", Value: "123", ValueType: model.TypeLong}
	for _, tg := range []model.Tag{testTag, long} {
		key, err := e.svc.StoreTag(e.ctx(), testSecretID, tg, authorizedSubject)
		if err != nil || key != tg.Key {
			t.Fatal(key, err)
		}
	}
	tags, err := e.svc.RetrieveTags(e.ctx(), testSecretID, authorizedSubject)
	if err != nil || len(tags) != 2 || tags[0] != testTag || tags[1] != long {
		t.Fatalf("%v %v", tags, err)
	}
	// StorageIT givenSuccessfulRequest_whenStoreTag_thenPersist: other tags stay retrievable
	got, err := e.svc.RetrieveTag(e.ctx(), testSecretID, testTag.Key, authorizedSubject)
	if err != nil || got != testTag {
		t.Fatal(got, err)
	}
}

// ---- replaceTags ----

func TestReplaceTagsDuplicateKeys(t *testing.T) {
	e := newEnv(t)
	dup := model.Tag{Key: testTag.Key, Value: "duplicate", ValueType: model.TypeString}
	err := e.svc.ReplaceTags(e.ctx(), testSecretID, []model.Tag{testTag, dup}, authorizedSubject)
	wantErr(t, err, model.KindBadRequest, TagsWithSameKeyMsg)
}

func TestReplaceTagsUnknownSecret(t *testing.T) {
	e := newEnv(t)
	for _, id := range []string{testSecretID, unknownID} {
		err := e.svc.ReplaceTags(e.ctx(), id, []model.Tag{testTag}, authorizedSubject)
		wantErr(t, err, model.KindNotFound, notFoundMsg(id))
		err = e.svc.ReplaceTags(e.ctx(), id, nil, authorizedSubject)
		wantErr(t, err, model.KindNotFound, notFoundMsg(id))
	}
}

func TestReplaceTagsNoAccess(t *testing.T) {
	e := newEnv(t)
	e.persist(testSecretID, testTag)
	err := e.svc.ReplaceTags(e.ctx(), testSecretID, []model.Tag{testTag, reservedCreationDate}, "someone-else")
	wantErr(t, err, model.KindUnauthorized, "User is not authorized to update tags for this secret")
	if got := e.dbTags(testSecretID); len(got) != 1 {
		t.Fatalf("tags must be unchanged: %v", got)
	}
}

// givenListHasTagWithReservedKey_whenReplaceTags_thenReplaceByExistingTagAndPersist: the supplied
// reserved tags are ignored, the existing creation-date/owner tags survive, everything else is replaced.
func TestReplaceTagsKeepsExistingReservedTags(t *testing.T) {
	e := newEnv(t)
	oldCreation := model.Tag{Key: CreationDateKey, Value: "1600000000000", ValueType: model.TypeString}
	owner := model.Tag{Key: opa.OwnerTagKey, Value: authorizedSubject, ValueType: model.TypeString}
	old := model.Tag{Key: "old", Value: "x", ValueType: model.TypeString}
	e.persist(testSecretID, old, oldCreation, owner)
	e.persist(testSecretID2, old)
	if err := e.svc.ReplaceTags(e.ctx(), testSecretID, []model.Tag{testTag, reservedCreationDate, reservedOwner}, authorizedSubject); err != nil {
		t.Fatal(err)
	}
	if got := e.dbTags(testSecretID); !sameTags(got, []model.Tag{testTag, oldCreation, owner}) {
		t.Fatalf("tags %v", got)
	}
	if got := e.dbTags(testSecretID2); len(got) != 1 || got[0] != old {
		t.Fatalf("other secrets must be untouched: %v", got)
	}
	// replacing by the empty list drops everything but the reserved tags
	if err := e.svc.ReplaceTags(e.ctx(), testSecretID, nil, authorizedSubject); err != nil {
		t.Fatal(err)
	}
	if got := e.dbTags(testSecretID); !sameTags(got, []model.Tag{oldCreation, owner}) {
		t.Fatalf("tags %v", got)
	}
	if c := e.opa.calls(); len(c) != 2 || c[0].Action != opa.TagUpdate {
		t.Fatalf("%+v", c)
	}
}

// Secrets without reserved tags (StorageIT fixtures): plain replacement.
func TestReplaceTagsWithoutReservedTags(t *testing.T) {
	e := newEnv(t)
	e.persist(testSecretID, testTag)
	if err := e.svc.ReplaceTags(e.ctx(), testSecretID, []model.Tag{testTag2}, authorizedSubject); err != nil {
		t.Fatal(err)
	}
	if got := e.dbTags(testSecretID); len(got) != 1 || got[0] != testTag2 {
		t.Fatalf("%v", got)
	}
}

// README "Differences": PUT /tags works unless COMPAT_JAVA_TAG_REPLACE_BUG, which reproduces the
// Hibernate ObjectDeletedException of the Java service for secrets that carry reserved tags.
func TestReplaceTagsJavaCompatBug(t *testing.T) {
	e := newEnv(t)
	e.svc.JavaTagReplaceBug = true
	owner := model.Tag{Key: opa.OwnerTagKey, Value: authorizedSubject, ValueType: model.TypeString}
	e.persist(testSecretID, testTag, owner)
	e.persist(testSecretID2, testTag)
	err := e.svc.ReplaceTags(e.ctx(), testSecretID, []model.Tag{testTag2}, authorizedSubject)
	var me *model.Error
	if !asModelErr(err, &me) || me.Kind != model.KindInternal || !strings.Contains(me.Msg, "ObjectDeletedException") {
		t.Fatalf("want ObjectDeletedException, got %v", err)
	}
	if got := e.dbTags(testSecretID); len(got) != 2 {
		t.Fatalf("failed replacement must not change tags: %v", got)
	}
	// without reserved tags the Java service succeeds
	if err := e.svc.ReplaceTags(e.ctx(), testSecretID2, []model.Tag{testTag2}, authorizedSubject); err != nil {
		t.Fatal(err)
	}
}

// ---- retrieveTags / retrieveTag ----

func TestRetrieveTagsUnknownSecret(t *testing.T) {
	e := newEnv(t)
	_, err := e.svc.RetrieveTags(e.ctx(), testSecretID, authorizedSubject)
	wantErr(t, err, model.KindNotFound, notFoundMsg(testSecretID))
}

func TestRetrieveTagsNoAccess(t *testing.T) {
	e := newEnv(t)
	e.persist(testSecretID, testTag)
	_, err := e.svc.RetrieveTags(e.ctx(), testSecretID, "someone-else")
	wantErr(t, err, model.KindUnauthorized, "User is not authorized to read tags for this secret")
}

func TestRetrieveTags(t *testing.T) {
	e := newEnv(t)
	e.persist(testSecretID, testTag, testTag2)
	tags, err := e.svc.RetrieveTags(e.ctx(), testSecretID, authorizedSubject)
	if err != nil || !sameTags(tags, []model.Tag{testTag, testTag2}) {
		t.Fatalf("%v %v", tags, err)
	}
	if c := e.opa.calls(); len(c) != 1 || c[0].Action != opa.TagRead || !sameTags(c[0].Tags, tags) {
		t.Fatalf("%+v", c)
	}
}

// StorageIT givenObjectWithoutTagsDefined_whenRetrieveTags_thenReturnEmptyList
func TestRetrieveTagsOfSecretWithoutTags(t *testing.T) {
	e := newEnv(t)
	e.persist(testSecretID)
	tags, err := e.svc.RetrieveTags(e.ctx(), testSecretID, authorizedSubject)
	if err != nil || tags == nil || len(tags) != 0 {
		t.Fatalf("want empty list, got %#v %v", tags, err)
	}
}

func TestRetrieveTagErrors(t *testing.T) {
	e := newEnv(t)
	_, err := e.svc.RetrieveTag(e.ctx(), testSecretID, testTag.Key, authorizedSubject)
	wantErr(t, err, model.KindNotFound, notFoundMsg(testSecretID))
	e.persist(testSecretID)
	_, err = e.svc.RetrieveTag(e.ctx(), testSecretID, testTag.Key, authorizedSubject)
	wantErr(t, err, model.KindNotFound, fmt.Sprintf(NoTagWithKeyMsg, testTag.Key, testSecretID))
	_, err = e.svc.RetrieveTag(e.ctx(), testSecretID, testTag.Key, "someone-else")
	wantErr(t, err, model.KindUnauthorized, "User is not authorized to read tags for this secret")
}

func TestRetrieveTag(t *testing.T) {
	e := newEnv(t)
	e.persist(testSecretID, testTag, testTag2)
	got, err := e.svc.RetrieveTag(e.ctx(), testSecretID, testTag.Key, authorizedSubject)
	if err != nil || got != testTag {
		t.Fatal(got, err)
	}
	// reserved tags are readable too
	e.persist(testSecretID2, model.Tag{Key: opa.OwnerTagKey, Value: "me", ValueType: model.TypeString})
	if got, err := e.svc.RetrieveTag(e.ctx(), testSecretID2, opa.OwnerTagKey, authorizedSubject); err != nil || got.Value != "me" {
		t.Fatal(got, err)
	}
}

// ---- updateTag ----

func TestUpdateTagReservedKey(t *testing.T) {
	e := newEnv(t)
	for _, r := range []model.Tag{reservedCreationDate, reservedOwner} {
		wantErr(t, e.svc.UpdateTag(e.ctx(), testSecretID, r, authorizedSubject), model.KindBadRequest, fmt.Sprintf(IsReservedKeyMsg, r.Key))
	}
}

func TestUpdateTagErrors(t *testing.T) {
	e := newEnv(t)
	wantErr(t, e.svc.UpdateTag(e.ctx(), testSecretID, testTag, authorizedSubject), model.KindNotFound, notFoundMsg(testSecretID))
	e.persist(testSecretID)
	wantErr(t, e.svc.UpdateTag(e.ctx(), testSecretID, testTag, authorizedSubject), model.KindNotFound, fmt.Sprintf(NoTagWithKeyMsg, testTag.Key, testSecretID))
	newTag := model.Tag{Key: testTag.Key, Value: "123", ValueType: model.TypeLong}
	wantErr(t, e.svc.UpdateTag(e.ctx(), testSecretID, newTag, "someone-else"), model.KindUnauthorized, "User is not authorized to update tags for this secret")
}

func TestUpdateTag(t *testing.T) {
	e := newEnv(t)
	e.persist(testSecretID, testTag, testTag2)
	newTag := model.Tag{Key: testTag.Key, Value: "123", ValueType: model.TypeLong}
	if err := e.svc.UpdateTag(e.ctx(), testSecretID, newTag, authorizedSubject); err != nil {
		t.Fatal(err)
	}
	if got := e.dbTags(testSecretID); !sameTags(got, []model.Tag{newTag, testTag2}) {
		t.Fatalf("%v", got)
	}
}

// ---- deleteTag ----

func TestDeleteTagReservedKey(t *testing.T) {
	e := newEnv(t)
	for _, k := range []string{CreationDateKey, opa.OwnerTagKey} {
		wantErr(t, e.svc.DeleteTag(e.ctx(), testSecretID, k, authorizedSubject), model.KindBadRequest, fmt.Sprintf(IsReservedKeyMsg, k))
	}
}

func TestDeleteTagErrors(t *testing.T) {
	e := newEnv(t)
	wantErr(t, e.svc.DeleteTag(e.ctx(), testSecretID, testTag.Key, authorizedSubject), model.KindNotFound, notFoundMsg(testSecretID))
	e.persist(testSecretID, testTag)
	e.persist(testSecretID2, testTag)
	// StorageIT givenObjectHasNoTagWithRequestedKey_whenDeleteKey_thenThrowNotFoundException
	wantErr(t, e.svc.DeleteTag(e.ctx(), testSecretID, "unknown_key", authorizedSubject), model.KindNotFound, fmt.Sprintf(NoTagWithKeyMsg, "unknown_key", testSecretID))
	wantErr(t, e.svc.DeleteTag(e.ctx(), testSecretID, testTag.Key, "someone-else"), model.KindUnauthorized, "User is not authorized to delete tags for this secret")
	if len(e.dbTags(testSecretID)) != 1 {
		t.Fatal("tag must stay")
	}
}

// StorageIT: givenSuccessfulRequest_whenDeleteTag_thenDoNoLongerReturn and
// givenMultipleObjectsWIthIdenticalTag_whenDeleteTagOnOneObject_thenKeepTagForOtherObjects
func TestDeleteTag(t *testing.T) {
	e := newEnv(t)
	e.persist(testSecretID, testTag, model.Tag{Key: testTag.Key + "new", Value: testTag.Value, ValueType: model.TypeString})
	e.persist(testSecretID2, testTag)
	if err := e.svc.DeleteTag(e.ctx(), testSecretID, testTag.Key, authorizedSubject); err != nil {
		t.Fatal(err)
	}
	tags, err := e.svc.RetrieveTags(e.ctx(), testSecretID, authorizedSubject)
	if err != nil || len(tags) != 1 || tags[0].Key != testTag.Key+"new" {
		t.Fatalf("%v %v", tags, err)
	}
	filt := []model.TagFilter{{Key: testTag.Key, Value: testTag.Value, Op: model.OpEquals}}
	p, err := e.svc.List(e.ctx(), listParams(filt))
	if err != nil || p.TotalElements != 1 || p.Content[0].SecretID != testSecretID2 {
		t.Fatalf("%+v %v", p, err)
	}
	all, _ := e.svc.List(e.ctx(), listParams(nil))
	if all.TotalElements != 2 {
		t.Fatalf("both secrets must remain: %+v", all)
	}
	// deleting the last tag leaves an empty list
	if err := e.svc.DeleteTag(e.ctx(), testSecretID2, testTag.Key, authorizedSubject); err != nil {
		t.Fatal(err)
	}
	if tags, _ := e.svc.RetrieveTags(e.ctx(), testSecretID2, authorizedSubject); len(tags) != 0 {
		t.Fatal(tags)
	}
}

// SPEC hazard H8: a 256 character value is valid for the model but exceeds varchar(255); the
// failure surfaces as a 500 and nothing is persisted.
func TestStoreTagValueExceedingColumnWidth(t *testing.T) {
	e := newEnv(t)
	e.persist(testSecretID)
	long := model.Tag{Key: "k", Value: strings.Repeat("a", 256), ValueType: model.TypeString}
	if err := long.ValueType.ValidateValue(long.Value); err != nil {
		t.Fatalf("model must accept 256 characters: %v", err)
	}
	_, err := e.svc.StoreTag(e.ctx(), testSecretID, long, authorizedSubject)
	var me *model.Error
	if !asModelErr(err, &me) || me.Kind != model.KindInternal || !strings.Contains(me.Msg, "DataException") {
		t.Fatalf("want 500 DataException, got %v", err)
	}
	if len(e.dbTags(testSecretID)) != 0 {
		t.Fatal("nothing may be persisted")
	}
	// 255 characters fit
	long.Value = strings.Repeat("a", 255)
	if _, err := e.svc.StoreTag(e.ctx(), testSecretID, long, authorizedSubject); err != nil {
		t.Fatal(err)
	}
}
