package pgstore

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Samrat7745/amphora/go/internal/model"
	"github.com/Samrat7745/amphora/go/internal/testkit"
	"github.com/jackc/pgx/v5/pgconn"
)

// Store level tests (schema as created by Hibernate ddl-auto=update, transactions, tag rows).
// Filtering/sorting/paging through Store.List is covered by FilterPagingAndNestedSortingIT's port in
// internal/service/storage_list_test.go.

var pgC = testkit.NewLazy(testkit.StartPostgres)

func TestMain(m *testing.M) {
	code := m.Run()
	pgC.Close()
	os.Exit(code)
}

func open(t *testing.T) (*Store, context.Context) {
	t.Helper()
	dsn := pgC.Get(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	s, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if _, err := s.Pool.Exec(ctx, "TRUNCATE secret_tags, amphora_secrets"); err != nil {
		t.Fatal(err)
	}
	return s, ctx
}

const id1 = "3bcf8308-8f50-4d24-a37b-b0075bb5e779"
const id2 = "0e7cd962-d98e-4eea-82ae-4641399c9ad7"

var tg = model.Tag{Key: "TEST_KEY", Value: "TEST_VALUE", ValueType: model.TypeString}

func TestSchemaMatchesHibernate(t *testing.T) {
	s, ctx := open(t)
	// reopening is non-destructive and idempotent
	if err := s.InsertSecret(ctx, s.Pool, id1); err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.Exists(ctx, s.Pool, id1); !ok {
		t.Fatal("EnsureSchema must not drop data")
	}
	cols := map[string]string{}
	rows, err := s.Pool.Query(ctx, "SELECT table_name||'.'||column_name, data_type||':'||coalesce(character_maximum_length::text,'')||':'||is_nullable FROM information_schema.columns WHERE table_schema='public'")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var k, v string
		_ = rows.Scan(&k, &v)
		cols[k] = v
	}
	rows.Close()
	want := map[string]string{
		"amphora_secrets.secret_id":  "character varying:255:NO",
		"secret_tags.id":             "integer::NO",
		"secret_tags.tag_key":        "character varying:255:YES",
		"secret_tags.tag_value":      "character varying:255:YES",
		"secret_tags.tag_value_type": "character varying:255:YES",
		"secret_tags.secret":         "character varying:255:NO",
	}
	for k, v := range want {
		if cols[k] != v {
			t.Errorf("column %s: got %q want %q", k, cols[k], v)
		}
	}
	var fk string
	if err := s.Pool.QueryRow(ctx, "SELECT conname FROM pg_constraint WHERE contype='f' AND conrelid='secret_tags'::regclass").Scan(&fk); err != nil || fk != "fk9i7dng41dtnf03ddv5wpv2qt6" {
		t.Fatalf("foreign key: %q %v", fk, err)
	}
	var seq string
	if err := s.Pool.QueryRow(ctx, "SELECT sequencename FROM pg_sequences WHERE sequencename='hibernate_sequence'").Scan(&seq); err != nil {
		t.Fatal("hibernate_sequence missing", err)
	}
}

func TestInsertFindUpdateDeleteTags(t *testing.T) {
	s, ctx := open(t)
	if err := s.InsertSecret(ctx, s.Pool, id1); err != nil {
		t.Fatal(err)
	}
	other := model.Tag{Key: "n", Value: "1", ValueType: model.TypeLong}
	for _, x := range []model.Tag{tg, other} {
		if err := s.InsertTag(ctx, s.Pool, id1, x); err != nil {
			t.Fatal(err)
		}
	}
	ok, rows, err := s.FindSecret(ctx, s.Pool, id1)
	if err != nil || !ok || len(rows) != 2 || rows[0].Tag() != tg || rows[1].Tag() != other || rows[0].ID >= rows[1].ID {
		t.Fatalf("%v %v %v", ok, rows, err)
	}
	if ok, rows, _ := s.FindSecret(ctx, s.Pool, id2); ok || rows != nil {
		t.Fatal("unknown secret")
	}
	f, err := s.FindTag(ctx, s.Pool, id1, "n")
	if err != nil || f == nil || f.Tag() != other {
		t.Fatal(f, err)
	}
	if f, err := s.FindTag(ctx, s.Pool, id1, "missing"); err != nil || f != nil {
		t.Fatal(f, err)
	}
	upd := model.Tag{Key: "n", Value: "abc", ValueType: model.TypeString}
	if err := s.UpdateTag(ctx, s.Pool, f0(t, s, ctx, "n").ID, upd); err != nil {
		t.Fatal(err)
	}
	if got := f0(t, s, ctx, "n").Tag(); got != upd {
		t.Fatalf("%v", got)
	}
	if err := s.DeleteTagRow(ctx, s.Pool, f0(t, s, ctx, "n").ID); err != nil {
		t.Fatal(err)
	}
	if rows, _ := s.Tags(ctx, s.Pool, id1); len(rows) != 1 || rows[0].Tag() != tg {
		t.Fatal(rows)
	}
}

func f0(t *testing.T, s *Store, ctx context.Context, key string) *TagRow {
	t.Helper()
	r, err := s.FindTag(ctx, s.Pool, id1, key)
	if err != nil || r == nil {
		t.Fatal(r, err)
	}
	return r
}

// DeleteTags keeps the given rows; with no rows to keep it removes every tag of the secret only.
func TestDeleteTags(t *testing.T) {
	s, ctx := open(t)
	for _, id := range []string{id1, id2} {
		if err := s.InsertSecret(ctx, s.Pool, id); err != nil {
			t.Fatal(err)
		}
		for _, k := range []string{"a", "b", "c"} {
			if err := s.InsertTag(ctx, s.Pool, id, model.Tag{Key: k, Value: "v", ValueType: model.TypeString}); err != nil {
				t.Fatal(err)
			}
		}
	}
	b := f0(t, s, ctx, "b")
	if err := s.DeleteTags(ctx, s.Pool, id1, []int32{b.ID}); err != nil {
		t.Fatal(err)
	}
	if rows, _ := s.Tags(ctx, s.Pool, id1); len(rows) != 1 || rows[0].Key != "b" {
		t.Fatalf("%v", rows)
	}
	if rows, _ := s.Tags(ctx, s.Pool, id2); len(rows) != 3 {
		t.Fatal("other secret untouched")
	}
	for _, keep := range [][]int32{nil, {}} {
		if err := s.InsertTag(ctx, s.Pool, id1, tg); err != nil {
			t.Fatal(err)
		}
		if err := s.DeleteTags(ctx, s.Pool, id1, keep); err != nil {
			t.Fatal(err)
		}
		if rows, _ := s.Tags(ctx, s.Pool, id1); keep == nil && len(rows) != 0 {
			t.Fatalf("nothing to keep must delete all tags, left %v", rows)
		}
	}
}

func TestDeleteSecretRemovesTags(t *testing.T) {
	s, ctx := open(t)
	_ = s.InsertSecret(ctx, s.Pool, id1)
	_ = s.InsertSecret(ctx, s.Pool, id2)
	_ = s.InsertTag(ctx, s.Pool, id1, tg)
	_ = s.InsertTag(ctx, s.Pool, id2, tg)
	if err := s.DeleteSecret(ctx, s.Pool, id1); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.Exists(ctx, s.Pool, id1); ok {
		t.Fatal("secret must be gone")
	}
	if rows, _ := s.Tags(ctx, s.Pool, id1); len(rows) != 0 {
		t.Fatal("tags must be gone")
	}
	if rows, _ := s.Tags(ctx, s.Pool, id2); len(rows) != 1 {
		t.Fatal("other tags stay")
	}
}

// the tag ids come from one sequence shared by all tags (Hibernate's hibernate_sequence)
func TestTagIDsFromSharedSequence(t *testing.T) {
	s, ctx := open(t)
	_ = s.InsertSecret(ctx, s.Pool, id1)
	_ = s.InsertSecret(ctx, s.Pool, id2)
	_ = s.InsertTag(ctx, s.Pool, id1, tg)
	_ = s.InsertTag(ctx, s.Pool, id2, tg)
	_ = s.InsertTag(ctx, s.Pool, id1, model.Tag{Key: "x", Value: "y", ValueType: model.TypeString})
	var distinct int
	if err := s.Pool.QueryRow(ctx, "SELECT count(DISTINCT id) FROM secret_tags").Scan(&distinct); err != nil || distinct != 3 {
		t.Fatal(distinct, err)
	}
}

func TestTransactionRollbackAndCommit(t *testing.T) {
	s, ctx := open(t)
	boom := errors.New("boom")
	err := s.InTx(ctx, func(q Querier) error {
		if err := s.InsertSecret(ctx, q, id1); err != nil {
			return err
		}
		if err := s.InsertTag(ctx, q, id1, tg); err != nil {
			return err
		}
		return boom
	})
	if err != boom {
		t.Fatal(err)
	}
	if ok, _ := s.Exists(ctx, s.Pool, id1); ok {
		t.Fatal("rollback expected")
	}
	if err := s.InTx(ctx, func(q Querier) error { return s.InsertSecret(ctx, q, id1) }); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.Exists(ctx, s.Pool, id1); !ok {
		t.Fatal("commit expected")
	}
}

func TestConstraints(t *testing.T) {
	s, ctx := open(t)
	// duplicate primary key
	_ = s.InsertSecret(ctx, s.Pool, id1)
	var pe *pgconn.PgError
	if err := s.InsertSecret(ctx, s.Pool, id1); !errors.As(err, &pe) || pe.Code != "23505" {
		t.Fatalf("want unique violation, got %v", err)
	}
	// tag of an unknown secret violates the foreign key
	if err := s.InsertTag(ctx, s.Pool, id2, tg); !errors.As(err, &pe) || pe.Code != "23503" {
		t.Fatalf("want FK violation, got %v", err)
	}
	// values are varchar(255): the model allows 256 characters (SPEC hazard), the database rejects them
	long := model.Tag{Key: "k", Value: strings.Repeat("a", 256), ValueType: model.TypeString}
	if err := s.InsertTag(ctx, s.Pool, id1, long); !errors.As(err, &pe) || pe.Code != "22001" {
		t.Fatalf("want value too long, got %v", err)
	}
	ok := model.Tag{Key: "k", Value: strings.Repeat("a", 255), ValueType: model.TypeString}
	if err := s.InsertTag(ctx, s.Pool, id1, ok); err != nil {
		t.Fatal(err)
	}
}

func TestPing(t *testing.T) {
	s, ctx := open(t)
	if err := s.Ping(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestDSN(t *testing.T) {
	got := DSN("db", 5432, "us er", "p@ss:w/rd", "amphora")
	if got != "postgres://us%20er:p%40ss%3Aw%2Frd@db:5432/amphora?sslmode=prefer" {
		t.Fatal(got)
	}
}

func TestOpenFailsForUnreachableDatabase(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := Open(ctx, "postgres://u:p@127.0.0.1:1/db?sslmode=disable&connect_timeout=2"); err == nil {
		t.Fatal("must fail")
	}
}
