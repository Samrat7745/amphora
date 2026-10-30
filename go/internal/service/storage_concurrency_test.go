package service

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/carbynestack/amphora-go/internal/model"
	"github.com/carbynestack/amphora-go/internal/opa"
)

// Concurrency behaviour of the storage service against real Postgres/Redis/Minio. Run with -race.

// Two concurrent createSecret calls for the same id: exactly one wins. The loser either loses at the existence
// check (AlreadyExists, the documented error) or at the primary key (constraint violation mapped to a 500 like
// Hibernate's ConstraintViolationException); it never succeeds and never corrupts the winner's rows.
func TestConcurrentCreateSecretSameID(t *testing.T) {
	e := newEnv(t)
	for rep := 0; rep < 5; rep++ {
		id := "6c0d0b9c-0000-4000-8000-00000000000" + string(rune('a'+rep))
		masks := e.cacheMasks(id, 2)
		mi := maskedInput(t, id, 2, testTag)
		const n = 6
		var wins int32
		errs := make([]error, n)
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				if _, err := e.svc.CreateSecret(e.ctx(), mi, authorizedSubject); err == nil {
					atomic.AddInt32(&wins, 1)
				} else {
					errs[i] = err
				}
			}(i)
		}
		close(start)
		wg.Wait()
		if wins != 1 {
			t.Fatalf("rep %d: %d winners (%v)", rep, wins, errs)
		}
		for _, err := range errs {
			if err == nil {
				continue
			}
			var me *model.Error
			if !asModelErr(err, &me) {
				t.Fatalf("not a model error: %v", err)
			}
			switch {
			case me.Kind == model.KindAlreadyExists && me.Msg == SecretWithIDExistsMsg:
			case me.Kind == model.KindInternal && strings.Contains(me.Msg, "ConstraintViolationException"):
			case me.Kind == model.KindInternal && strings.Contains(me.Msg, "No InputMaskList available"):
				// the winner already consumed the masks before this caller read them
			default:
				t.Fatalf("unexpected loser error: kind=%d %q", me.Kind, me.Msg)
			}
		}
		// the stored secret is the winner's: one creation date, one owner, user tag, and consistent data
		tags := e.dbTags(id)
		count := map[string]int{}
		for _, tg := range tags {
			count[tg.Key]++
		}
		if len(tags) != 3 || count[CreationDateKey] != 1 || count[opa.OwnerTagKey] != 1 || count[testTag.Key] != 1 {
			t.Fatalf("tags: %v", tags)
		}
		if want := e.expectedShare(t, mi, masks); !e.hasData(id) {
			t.Fatal("data missing")
		} else if got, err := e.obj.Get(e.ctx(), id); err != nil || string(got) != string(want) {
			t.Fatalf("data differs from the winner's share: %v", err)
		}
	}
}

// Concurrent tag creation with the same key: one wins, the others get AlreadyExists; distinct keys all succeed.
func TestConcurrentStoreTag(t *testing.T) {
	e := newEnv(t)
	e.persist(testSecretID)
	var wins, exists int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := e.svc.StoreTag(e.ctx(), testSecretID, model.Tag{Key: "same", Value: "v", ValueType: model.TypeString}, authorizedSubject)
			var me *model.Error
			switch {
			case err == nil:
				atomic.AddInt32(&wins, 1)
			case asModelErr(err, &me) && me.Kind == model.KindAlreadyExists:
				atomic.AddInt32(&exists, 1)
			default:
				t.Errorf("unexpected: %v", err)
			}
		}()
	}
	wg.Wait()
	// Regression: tag_key is not unique in the schema; without the row lock in StoreTag two racing inserts both
	// passed FindTag and created duplicate rows.
	if wins != 1 || exists != 7 {
		t.Fatalf("wins=%d exists=%d", wins, exists)
	}
	if got := e.dbTags(testSecretID); len(got) != 1 {
		t.Fatalf("duplicate tag rows: %v", got)
	}

	e.persist("other-id")
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			k := "k" + string(rune('a'+i))
			if _, err := e.svc.StoreTag(e.ctx(), "other-id", model.Tag{Key: k, Value: "1", ValueType: model.TypeLong}, authorizedSubject); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	if got := e.dbTags("other-id"); len(got) != 8 {
		t.Fatalf("%v", got)
	}
}

// Concurrent updates of the same tag: all succeed, the final value is one of the written values.
func TestConcurrentUpdateTag(t *testing.T) {
	e := newEnv(t)
	e.persist(testSecretID, model.Tag{Key: "k", Value: "0", ValueType: model.TypeLong})
	var wg sync.WaitGroup
	for i := 1; i <= 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			v := string(rune('0' + i%10))
			if err := e.svc.UpdateTag(e.ctx(), testSecretID, model.Tag{Key: "k", Value: v, ValueType: model.TypeLong}, authorizedSubject); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	tags := e.dbTags(testSecretID)
	if len(tags) != 1 || tags[0].ValueType != model.TypeLong || len(tags[0].Value) != 1 {
		t.Fatalf("%v", tags)
	}
}

// Concurrent replaceTags and tag creation must neither deadlock nor lose the reserved tags.
func TestConcurrentReplaceTagsKeepsReservedTags(t *testing.T) {
	e := newEnv(t)
	id := "6c0d0b9c-1111-4000-8000-000000000001"
	e.cacheMasks(id, 1)
	if _, err := e.svc.CreateSecret(e.ctx(), maskedInput(t, id, 1), authorizedSubject); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tags := []model.Tag{{Key: "a", Value: string(rune('0' + i)), ValueType: model.TypeString}}
			if err := e.svc.ReplaceTags(e.ctx(), id, tags, authorizedSubject); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	tags := e.dbTags(id)
	if _, ok := tagByKey(tags, CreationDateKey); !ok {
		t.Fatalf("creation date lost: %v", tags)
	}
	if _, ok := tagByKey(tags, opa.OwnerTagKey); !ok {
		t.Fatalf("owner lost: %v", tags)
	}
}

// Deleting a secret while readers fetch it: every reader gets the full share or a clean not-found/500 error,
// after the delete everything is gone, and no reader panics or observes a half deleted secret as success.
func TestDeleteWhileReading(t *testing.T) {
	e := newEnv(t)
	id := "6c0d0b9c-2222-4000-8000-000000000002"
	e.cacheMasks(id, 2)
	mi := maskedInput(t, id, 2, testTag)
	if _, err := e.svc.CreateSecret(e.ctx(), mi, authorizedSubject); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	stop := make(chan struct{})
	var reads, notFound int32
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				sh, err := e.svc.UseSecretShare(e.ctx(), id, authorizedSubject)
				var me *model.Error
				switch {
				case err == nil:
					if len(sh.Data) != 2*32 || len(sh.Tags) != 3 {
						t.Errorf("partial share: data=%d tags=%d", len(sh.Data), len(sh.Tags))
						return
					}
					atomic.AddInt32(&reads, 1)
				case asModelErr(err, &me) && me.Kind == model.KindNotFound:
					atomic.AddInt32(&notFound, 1)
					return
				case asModelErr(err, &me) && me.Kind == model.KindInternal:
					// data deleted between the metadata and the data read: surfaced as an error, not a panic
					atomic.AddInt32(&notFound, 1)
					return
				default:
					t.Errorf("unexpected: %v", err)
					return
				}
			}
		}()
	}
	if err := e.svc.DeleteSecret(e.ctx(), id, authorizedSubject); err != nil {
		t.Fatal(err)
	}
	wg.Wait() // readers exit on their first not-found
	close(stop)
	if e.exists(id) || e.hasData(id) {
		t.Fatal("secret must be gone")
	}
	if _, err := e.svc.GetSecretShare(e.ctx(), id, authorizedSubject); err == nil {
		t.Fatal("deleted secret readable")
	}
	t.Logf("reads before delete: %d", reads)
}

// Two concurrent deletes of the same secret: at least one succeeds, the other fails cleanly.
func TestConcurrentDeleteSameSecret(t *testing.T) {
	e := newEnv(t)
	id := "6c0d0b9c-3333-4000-8000-000000000003"
	e.cacheMasks(id, 1)
	if _, err := e.svc.CreateSecret(e.ctx(), maskedInput(t, id, 1), authorizedSubject); err != nil {
		t.Fatal(err)
	}
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = e.svc.DeleteSecret(e.ctx(), id, authorizedSubject)
		}(i)
	}
	wg.Wait()
	if errs[0] != nil && errs[1] != nil {
		t.Fatalf("both failed: %v %v", errs[0], errs[1])
	}
	if e.exists(id) || e.hasData(id) {
		t.Fatal("secret must be gone")
	}
}
