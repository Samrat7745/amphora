package pgstore

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Samrat7745/amphora/go/internal/model"
	"github.com/jackc/pgx/v5/pgconn"
)

// Direct query tests for Store.List and the tag/secret mutators against a real Postgres.

func seed(t *testing.T, s *Store, ctx context.Context, id string, tags ...model.Tag) {
	t.Helper()
	if err := s.InsertSecret(ctx, s.Pool, id); err != nil {
		t.Fatal(err)
	}
	for _, tg := range tags {
		if err := s.InsertTag(ctx, s.Pool, id, tg); err != nil {
			t.Fatal(err)
		}
	}
}

func lt(k, v string, vt model.TagValueType) model.Tag {
	return model.Tag{Key: k, Value: v, ValueType: vt}
}

func ids(p model.MetadataPage) []string {
	out := []string{}
	for _, m := range p.Content {
		out = append(out, m.SecretID)
	}
	return out
}

func sid(i int) string { return fmt.Sprintf("s%02d", i) }

func filt(k string, op model.TagFilterOperator, v string) model.TagFilter {
	return model.TagFilter{Key: k, Op: op, Value: v}
}

func TestListFilters(t *testing.T) {
	s, ctx := open(t)
	seed(t, s, ctx, "a", lt("n", "5", model.TypeLong), lt("name", "x", model.TypeString))
	seed(t, s, ctx, "b", lt("n", "10", model.TypeLong), lt("name", "10", model.TypeString))
	seed(t, s, ctx, "c", lt("n", "-3", model.TypeLong), lt("name", "y", model.TypeString))
	seed(t, s, ctx, "d")
	run := func(fs ...model.TagFilter) []string {
		t.Helper()
		p, err := s.List(ctx, ListParams{Filters: fs, SortKey: ""})
		if err != nil {
			t.Fatal(err)
		}
		got := ids(p)
		// order undefined without a sort: normalise
		sortStrings(got)
		return got
	}
	cases := []struct {
		name string
		f    []model.TagFilter
		want []string
	}{
		{"none", nil, []string{"a", "b", "c", "d"}},
		{"eq string", []model.TagFilter{filt("name", model.OpEquals, "x")}, []string{"a"}},
		{"eq string looks numeric", []model.TagFilter{filt("name", model.OpEquals, "10")}, []string{"b"}},
		{"eq long as text", []model.TagFilter{filt("n", model.OpEquals, "5")}, []string{"a"}},
		{"eq long non-canonical text does not match", []model.TagFilter{filt("n", model.OpEquals, "05")}, []string{}},
		{"eq no match", []model.TagFilter{filt("name", model.OpEquals, "zzz")}, []string{}},
		{"eq missing key", []model.TagFilter{filt("nokey", model.OpEquals, "x")}, []string{}},
		{"gt", []model.TagFilter{filt("n", model.OpGreaterThan, "5")}, []string{"b"}},
		{"gt negative", []model.TagFilter{filt("n", model.OpGreaterThan, "-4")}, []string{"a", "b", "c"}},
		{"lt", []model.TagFilter{filt("n", model.OpLessThan, "10")}, []string{"a", "c"}},
		{"lt boundary excluded", []model.TagFilter{filt("n", model.OpLessThan, "-3")}, []string{}},
		{"and", []model.TagFilter{filt("n", model.OpGreaterThan, "0"), filt("name", model.OpEquals, "x")}, []string{"a"}},
		{"and empty", []model.TagFilter{filt("n", model.OpGreaterThan, "6"), filt("name", model.OpEquals, "x")}, []string{}},
		{"same key twice (range)", []model.TagFilter{filt("n", model.OpGreaterThan, "0"), filt("n", model.OpLessThan, "10")}, []string{"a"}},
		{"gt on max int64", []model.TagFilter{filt("n", model.OpGreaterThan, "9223372036854775807")}, []string{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := run(c.f...); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %v want %v", got, c.want)
			}
		})
	}
}

func sortStrings(a []string) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j] < a[j-1]; j-- {
			a[j], a[j-1] = a[j-1], a[j]
		}
	}
}

// A numeric comparison on a key whose values are not numeric makes Postgres fail the cast. The service layer
// rejects non-numeric filter values for < and >, but tags of type STRING can be compared by key. Pin the behaviour.
func TestListNumericFilterOnStringValues(t *testing.T) {
	s, ctx := open(t)
	seed(t, s, ctx, "a", lt("k", "abc", model.TypeString))
	seed(t, s, ctx, "b", lt("k", "7", model.TypeString))
	_, err := s.List(ctx, ListParams{Filters: []model.TagFilter{filt("k", model.OpGreaterThan, "1")}})
	var pe *pgconn.PgError
	if err == nil || !errors.As(err, &pe) || pe.Code != "22P02" {
		t.Fatalf("want invalid_text_representation (22P02), got %v", err)
	}
	// when only numeric-looking STRING values exist the comparison works
	if _, err := s.Pool.Exec(ctx, "DELETE FROM secret_tags WHERE tag_value='abc'"); err != nil {
		t.Fatal(err)
	}
	p, err := s.List(ctx, ListParams{Filters: []model.TagFilter{filt("k", model.OpGreaterThan, "1")}})
	if err != nil || !reflect.DeepEqual(ids(p), []string{"b"}) {
		t.Fatalf("%v %v", ids(p), err)
	}
}

func TestListSorting(t *testing.T) {
	s, ctx := open(t)
	// LONG ordering is numeric (2 < 10), ties keep a stable set, missing tag sorts as NULL
	seed(t, s, ctx, "a", lt("n", "10", model.TypeLong))
	seed(t, s, ctx, "b", lt("n", "2", model.TypeLong))
	seed(t, s, ctx, "c", lt("n", "10", model.TypeLong))
	seed(t, s, ctx, "d") // no tag n
	seed(t, s, ctx, "e", lt("n", "-1", model.TypeLong))
	asc, err := s.List(ctx, ListParams{SortKey: "n"})
	if err != nil {
		t.Fatal(err)
	}
	g := ids(asc)
	if len(g) != 5 || g[0] != "e" || g[1] != "b" || g[4] != "d" {
		t.Fatalf("asc (NULL last in postgres): %v", g)
	}
	if !(g[2] == "a" && g[3] == "c" || g[2] == "c" && g[3] == "a") {
		t.Fatalf("ties: %v", g)
	}
	desc, err := s.List(ctx, ListParams{SortKey: "n", SortDesc: true})
	if err != nil {
		t.Fatal(err)
	}
	g = ids(desc)
	if g[0] != "d" || g[1] != "a" && g[1] != "c" || g[4] != "e" || g[3] != "b" {
		t.Fatalf("desc (NULL first in postgres): %v", g)
	}
	// STRING ordering is lexicographic
	if _, err := s.Pool.Exec(ctx, "TRUNCATE secret_tags, amphora_secrets"); err != nil {
		t.Fatal(err)
	}
	seed(t, s, ctx, "a", lt("n", "10", model.TypeString))
	seed(t, s, ctx, "b", lt("n", "2", model.TypeString))
	p, _ := s.List(ctx, ListParams{SortKey: "n"})
	if got := ids(p); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("string order %v", got)
	}
	// sorting by a key nobody has is a no-op sort
	p, err = s.List(ctx, ListParams{SortKey: "nokey"})
	if err != nil || len(p.Content) != 2 {
		t.Fatalf("%v %v", p, err)
	}
}

func TestListSortMixedTypesIsInternalError(t *testing.T) {
	s, ctx := open(t)
	seed(t, s, ctx, "a", lt("n", "1", model.TypeLong))
	seed(t, s, ctx, "b", lt("n", "x", model.TypeString))
	_, err := s.List(ctx, ListParams{SortKey: "n"})
	if err == nil || !strings.Contains(err.Error(), "different configurations") {
		t.Fatalf("%v", err)
	}
}

func TestListSortWithFilter(t *testing.T) {
	s, ctx := open(t)
	for i := 0; i < 6; i++ {
		seed(t, s, ctx, sid(i), lt("n", fmt.Sprint(i), model.TypeLong), lt("even", fmt.Sprint(i%2 == 0), model.TypeString))
	}
	p, err := s.List(ctx, ListParams{Filters: []model.TagFilter{filt("even", model.OpEquals, "true")}, SortKey: "n", SortDesc: true})
	if err != nil || !reflect.DeepEqual(ids(p), []string{"s04", "s02", "s00"}) || p.TotalElements != 3 {
		t.Fatalf("%v %v", p, err)
	}
}

func TestListPaging(t *testing.T) {
	s, ctx := open(t)
	for i := 0; i < 5; i++ {
		seed(t, s, ctx, sid(i), lt("n", fmt.Sprint(i), model.TypeLong))
	}
	type want struct {
		ids             []string
		number, size    int32
		elements, pages int64
	}
	cases := []struct {
		name       string
		page, size int
		w          want
	}{
		{"first page", 0, 2, want{[]string{"s00", "s01"}, 0, 2, 5, 3}},
		{"middle", 1, 2, want{[]string{"s02", "s03"}, 1, 2, 5, 3}},
		{"last partial", 2, 2, want{[]string{"s04"}, 2, 2, 5, 3}},
		{"size 1", 3, 1, want{[]string{"s03"}, 3, 1, 5, 5}},
		{"size larger than total", 0, 100, want{[]string{"s00", "s01", "s02", "s03", "s04"}, 0, 100, 5, 1}},
		{"exact multiple", 1, 5, want{[]string{}, 1, 5, 5, 1}},
		{"past the end", 7, 2, want{[]string{}, 7, 2, 5, 3}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, err := s.List(ctx, ListParams{SortKey: "n", Paged: true, Page: c.page, PageSize: c.size})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(ids(p), c.w.ids) || p.Number != c.w.number || p.Size != c.w.size || p.TotalElements != c.w.elements || p.TotalPages != int32(c.w.pages) {
				t.Fatalf("got ids=%v number=%d size=%d elements=%d pages=%d", ids(p), p.Number, p.Size, p.TotalElements, p.TotalPages)
			}
		})
	}
	// page 0 / unpaged: single page with everything
	p, err := s.List(ctx, ListParams{})
	if err != nil || len(p.Content) != 5 || p.Number != 0 || p.TotalPages != 1 || p.Size != maxInt32 {
		t.Fatalf("%v %v", p, err)
	}
	// page size 0 yields no content and one page (Spring unpaged-style)
	p, err = s.List(ctx, ListParams{Paged: true, Page: 0, PageSize: 0})
	if err != nil || len(p.Content) != 0 || p.TotalPages != 1 {
		t.Fatalf("%v %v", p, err)
	}
	// empty store
	if _, err := s.Pool.Exec(ctx, "TRUNCATE secret_tags, amphora_secrets"); err != nil {
		t.Fatal(err)
	}
	p, err = s.List(ctx, ListParams{Paged: true, Page: 0, PageSize: 3})
	if err != nil || len(p.Content) != 0 || p.TotalElements != 0 || p.TotalPages != 0 {
		t.Fatalf("%v %v", p, err)
	}
	if p.Content == nil {
		t.Fatal("content must be an empty list, not nil")
	}
}

func TestListReturnsTagsInInsertionOrder(t *testing.T) {
	s, ctx := open(t)
	seed(t, s, ctx, "a", lt("z", "1", model.TypeString), lt("a", "2", model.TypeLong), lt("m", "3", model.TypeString))
	seed(t, s, ctx, "b")
	p, err := s.List(ctx, ListParams{SortKey: "z"})
	if err != nil || len(p.Content) != 2 {
		t.Fatal(p, err)
	}
	var a, b model.Metadata
	for _, m := range p.Content {
		if m.SecretID == "a" {
			a = m
		} else {
			b = m
		}
	}
	if len(a.Tags) != 3 || a.Tags[0].Key != "z" || a.Tags[1].Key != "a" || a.Tags[2].Key != "m" {
		t.Fatalf("%v", a.Tags)
	}
	if b.Tags == nil || len(b.Tags) != 0 {
		t.Fatalf("secret without tags must have an empty non-nil list, got %#v", b.Tags)
	}
}

func TestDeleteTagsKeepList(t *testing.T) {
	s, ctx := open(t)
	seed(t, s, ctx, id1, lt("a", "1", model.TypeString), lt("b", "2", model.TypeString), lt("c", "3", model.TypeString))
	seed(t, s, ctx, id2, lt("a", "1", model.TypeString))
	rows, _ := s.Tags(ctx, s.Pool, id1)
	// keep two
	if err := s.DeleteTags(ctx, s.Pool, id1, []int32{rows[0].ID, rows[2].ID}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Tags(ctx, s.Pool, id1)
	if len(got) != 2 || got[0].Key != "a" || got[1].Key != "c" {
		t.Fatalf("%v", got)
	}
	// unknown ids in the keep list are harmless; other secrets are untouched
	if err := s.DeleteTags(ctx, s.Pool, id1, []int32{got[0].ID, 99999}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Tags(ctx, s.Pool, id1); len(got) != 1 || got[0].Key != "a" {
		t.Fatalf("%v", got)
	}
	if got, _ := s.Tags(ctx, s.Pool, id2); len(got) != 1 {
		t.Fatal("other secret's tags must stay")
	}
	// regression: a nil keep-list must delete everything (a NULL array would delete nothing)
	if err := s.DeleteTags(ctx, s.Pool, id1, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Tags(ctx, s.Pool, id1); len(got) != 0 {
		t.Fatalf("nil keep list must delete all, left %v", got)
	}
	if got, _ := s.Tags(ctx, s.Pool, id2); len(got) != 1 {
		t.Fatal("other secret's tags must stay")
	}
	// empty keep-list too, and deleting from an unknown secret is a no-op
	if err := s.DeleteTags(ctx, s.Pool, id2, []int32{}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteTags(ctx, s.Pool, "unknown", nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Tags(ctx, s.Pool, id2); len(got) != 0 {
		t.Fatal(got)
	}
}

func TestDeleteUnknownSecretAndTagRow(t *testing.T) {
	s, ctx := open(t)
	if err := s.DeleteSecret(ctx, s.Pool, "nope"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteTagRow(ctx, s.Pool, 424242); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateTag(ctx, s.Pool, 424242, tg); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.Exists(ctx, s.Pool, "nope"); ok || err != nil {
		t.Fatal(ok, err)
	}
}

func TestLongAndUnicodeValues(t *testing.T) {
	s, ctx := open(t)
	long255 := strings.Repeat("x", 255)
	seed(t, s, ctx, id1)
	var pe *pgconn.PgError
	// secret ids are varchar(255) too
	if err := s.InsertSecret(ctx, s.Pool, strings.Repeat("i", 256)); !errors.As(err, &pe) || pe.Code != "22001" {
		t.Fatalf("256 char id: %v", err)
	}
	if err := s.InsertSecret(ctx, s.Pool, strings.Repeat("i", 255)); err != nil {
		t.Fatal(err)
	}
	// key and value at the column limits
	if err := s.InsertTag(ctx, s.Pool, id1, lt(strings.Repeat("k", 255), long255, model.TypeString)); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertTag(ctx, s.Pool, id1, lt(strings.Repeat("k", 256), "v", model.TypeString)); !errors.As(err, &pe) || pe.Code != "22001" {
		t.Fatalf("256 char key: %v", err)
	}
	// unicode (multi byte, 255 characters is within varchar(255) even though > 255 bytes)
	uni := strings.Repeat("ü", 255)
	for _, tg := range []model.Tag{lt("schlüssel", "wert-ß", model.TypeString), lt("日本語", "値🙂", model.TypeString), lt("u", uni, model.TypeString)} {
		if err := s.InsertTag(ctx, s.Pool, id1, tg); err != nil {
			t.Fatal(tg.Key, err)
		}
	}
	rows, err := s.Tags(ctx, s.Pool, id1)
	if err != nil || len(rows) != 4 || rows[0].Value != long255 || rows[1].Key != "schlüssel" || rows[2].Value != "値🙂" || rows[3].Value != uni {
		t.Fatalf("%v %v", rows, err)
	}
	p, err := s.List(ctx, ListParams{Filters: []model.TagFilter{filt("日本語", model.OpEquals, "値🙂")}})
	if err != nil || !reflect.DeepEqual(ids(p), []string{id1}) {
		t.Fatalf("%v %v", ids(p), err)
	}
	// NUL bytes are rejected by Postgres text
	if err := s.InsertTag(ctx, s.Pool, id1, lt("k", "a\x00b", model.TypeString)); err == nil {
		t.Fatal("NUL byte must be rejected")
	}
}

func TestNullColumnsReadAsEmpty(t *testing.T) {
	s, ctx := open(t)
	seed(t, s, ctx, id1)
	if _, err := s.Pool.Exec(ctx, "INSERT INTO secret_tags (id, secret) VALUES (nextval('hibernate_sequence'), $1)", id1); err != nil {
		t.Fatal(err)
	}
	rows, err := s.Tags(ctx, s.Pool, id1)
	if err != nil || len(rows) != 1 || rows[0].Key != "" || rows[0].Value != "" || rows[0].ValueType != "" {
		t.Fatalf("%v %v", rows, err)
	}
	p, err := s.List(ctx, ListParams{})
	if err != nil || len(p.Content[0].Tags) != 1 {
		t.Fatal(p, err)
	}
}

func TestUniqueViolationInTxRollsBack(t *testing.T) {
	s, ctx := open(t)
	seed(t, s, ctx, id1)
	err := s.InTx(ctx, func(q Querier) error {
		if err := s.InsertSecret(ctx, q, id2); err != nil {
			return err
		}
		return s.InsertSecret(ctx, q, id1) // duplicate
	})
	var pe *pgconn.PgError
	if !errors.As(err, &pe) || pe.Code != "23505" {
		t.Fatalf("%v", err)
	}
	if ok, _ := s.Exists(ctx, s.Pool, id2); ok {
		t.Fatal("first insert must be rolled back")
	}
}

func TestConcurrentInsertSameSecret(t *testing.T) {
	s, ctx := open(t)
	const n = 8
	var wins, dups int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			err := s.InTx(ctx, func(q Querier) error {
				if err := s.InsertSecret(ctx, q, id1); err != nil {
					return err
				}
				return s.InsertTag(ctx, q, id1, tg)
			})
			var pe *pgconn.PgError
			switch {
			case err == nil:
				atomic.AddInt32(&wins, 1)
			case errors.As(err, &pe) && pe.Code == "23505":
				atomic.AddInt32(&dups, 1)
			default:
				t.Errorf("unexpected: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()
	if wins != 1 || dups != n-1 {
		t.Fatalf("wins=%d dups=%d", wins, dups)
	}
	if rows, _ := s.Tags(ctx, s.Pool, id1); len(rows) != 1 {
		t.Fatalf("exactly one tag expected, got %v", rows)
	}
}

func TestConcurrentTagIDsAreUnique(t *testing.T) {
	s, ctx := open(t)
	seed(t, s, ctx, id1)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := s.InsertTag(ctx, s.Pool, id1, lt(fmt.Sprint("k", i), "v", model.TypeString)); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	rows, _ := s.Tags(ctx, s.Pool, id1)
	seen := map[int32]bool{}
	for _, r := range rows {
		seen[r.ID] = true
	}
	if len(rows) != 20 || len(seen) != 20 {
		t.Fatalf("%d rows %d ids", len(rows), len(seen))
	}
}

func TestDSNEscaping(t *testing.T) {
	got := DSN("h", 5432, "us er", "p@ss:w/rd", "db")
	if got != "postgres://us%20er:p%40ss%3Aw%2Frd@h:5432/db?sslmode=prefer" {
		t.Fatal(got)
	}
	if (TagRow{Key: "k", Value: "v", ValueType: "LONG"}).Tag() != lt("k", "v", model.TypeLong) {
		t.Fatal("Tag()")
	}
}

func TestClosedStoreErrors(t *testing.T) {
	s, ctx := open(t)
	s.Pool.Close()
	if err := s.Ping(ctx); err == nil {
		t.Fatal("ping on closed pool")
	}
	if _, err := s.List(ctx, ListParams{}); err == nil {
		t.Fatal("list on closed pool")
	}
	if _, err := s.List(ctx, ListParams{SortKey: "k"}); err == nil {
		t.Fatal("sorted list on closed pool")
	}
	if _, _, err := s.FindSecret(ctx, s.Pool, "x"); err == nil {
		t.Fatal("find on closed pool")
	}
	if _, err := s.Tags(ctx, s.Pool, "x"); err == nil {
		t.Fatal("tags")
	}
	if _, err := s.FindTag(ctx, s.Pool, "x", "k"); err == nil {
		t.Fatal("findtag")
	}
	if err := s.InTx(ctx, func(Querier) error { return nil }); err == nil {
		t.Fatal("tx")
	}
	if err := s.DeleteSecret(ctx, s.Pool, "x"); err == nil {
		t.Fatal("delete")
	}
	if err := s.EnsureSchema(ctx); err == nil {
		t.Fatal("schema")
	}
}
