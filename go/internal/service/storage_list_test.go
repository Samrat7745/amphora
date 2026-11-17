package service

// Port of FilterPagingAndNestedSortingIT (filtering, sorting and nested sorting by tag value).

import (
	"fmt"
	"testing"

	"github.com/Samrat7745/amphora/go/internal/model"
	"github.com/Samrat7745/amphora/go/internal/pgstore"
)

const longKey = "LONG_KEY"

var (
	tagLong99  = model.Tag{Key: longKey, Value: "99", ValueType: model.TypeString} // Tag.builder() default type
	tagLong100 = model.Tag{Key: longKey, Value: "100", ValueType: model.TypeString}
)

func eq(t model.Tag) model.TagFilter {
	return model.TagFilter{Key: t.Key, Value: t.Value, Op: model.OpEquals}
}

func listIDs(t *testing.T, e *env, p pgstore.ListParams) []string {
	t.Helper()
	page, err := e.svc.List(e.ctx(), p)
	if err != nil {
		t.Fatal(err)
	}
	return idsOf(page)
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	m := map[string]int{}
	for _, x := range a {
		m[x]++
	}
	for _, x := range b {
		m[x]--
	}
	for _, c := range m {
		if c != 0 {
			return false
		}
	}
	return true
}

// ...OnlyOneMatchingTagFilter_whenGetObjectListWithFilter_thenReturnExpectedContent
func TestFilterOnlyOneMatching(t *testing.T) {
	e := newEnv(t)
	e.persist(testSecretID, testTag, testTag2)
	e.persist(testSecretID2, testTag, testTag3)
	p, err := e.svc.List(e.ctx(), listParams([]model.TagFilter{eq(testTag2)}))
	if err != nil || p.TotalElements != 1 {
		t.Fatalf("%+v %v", p, err)
	}
	if m := p.Content[0]; m.SecretID != testSecretID || !sameTags(m.Tags, []model.Tag{testTag, testTag2}) {
		t.Fatalf("%+v", m)
	}
}

// ...NoneMatchingFilterCriteria: filters are combined with AND
func TestFilterNoneMatching(t *testing.T) {
	e := newEnv(t)
	e.persist(testSecretID, testTag, testTag2)
	e.persist(testSecretID2, testTag, testTag3)
	p, err := e.svc.List(e.ctx(), listParams([]model.TagFilter{eq(testTag2), eq(testTag3)}))
	if err != nil || p.TotalElements != 0 || len(p.Content) != 0 {
		t.Fatalf("%+v %v", p, err)
	}
}

func TestFilterMultipleMatching(t *testing.T) {
	e := newEnv(t)
	e.persist(testSecretID, testTag, testTag2)
	e.persist(testSecretID2, testTag, testTag3)
	e.persist(testSecretID3, testTag2, testTag3)
	p, err := e.svc.List(e.ctx(), listParams([]model.TagFilter{eq(testTag)}))
	if err != nil || p.TotalElements != 2 {
		t.Fatalf("%+v %v", p, err)
	}
	byID := map[string][]model.Tag{}
	for _, m := range p.Content {
		byID[m.SecretID] = m.Tags
	}
	if !sameTags(byID[testSecretID], []model.Tag{testTag, testTag2}) || !sameTags(byID[testSecretID2], []model.Tag{testTag, testTag3}) || len(byID) != 2 {
		t.Fatalf("%v", byID)
	}
}

func TestFilterLongComparisons(t *testing.T) {
	for _, tc := range []struct {
		name     string
		seed     func(e *env)
		filter   model.TagFilter
		wantSecs []string
	}{
		{"long less than", func(e *env) {
			e.persist(testSecretID, tagLong99)
			e.persist(testSecretID2, tagLong100)
		}, model.TagFilter{Key: longKey, Value: "1000", Op: model.OpLessThan}, []string{testSecretID, testSecretID2}},
		{"mixed tag value types", func(e *env) {
			e.persist(testSecretID, tagLong99)
			e.persist(testSecretID2, testTag, tagLong100)
		}, model.TagFilter{Key: longKey, Value: "1000", Op: model.OpLessThan}, []string{testSecretID, testSecretID2}},
		{"long greater than", func(e *env) {
			e.persist(testSecretID, testTag2, tagLong99)
			e.persist(testSecretID2, tagLong100)
		}, model.TagFilter{Key: longKey, Value: "99", Op: model.OpGreaterThan}, []string{testSecretID2}},
		{"value equal to the bound is excluded", func(e *env) {
			e.persist(testSecretID, testTag, tagLong99)
			e.persist(testSecretID2, testTag, tagLong100)
		}, model.TagFilter{Key: longKey, Value: "99", Op: model.OpGreaterThan}, []string{testSecretID2}},
		{"less than excludes the bound too", func(e *env) {
			e.persist(testSecretID, tagLong99)
			e.persist(testSecretID2, tagLong100)
		}, model.TagFilter{Key: longKey, Value: "100", Op: model.OpLessThan}, []string{testSecretID}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			tc.seed(e)
			p, err := e.svc.List(e.ctx(), listParams([]model.TagFilter{tc.filter}))
			if err != nil || p.TotalElements != int64(len(tc.wantSecs)) || !sameSet(idsOf(p), tc.wantSecs) {
				t.Fatalf("%+v %v", p, err)
			}
		})
	}
}

// ...givenNumberComparisonFilterWithNonParsableValue_whenCreateTagFilter_thenThrowIllegalArgumentException
func TestNumberComparisonFilterWithNonParsableValue(t *testing.T) {
	for _, op := range []string{"<", ">"} {
		_, err := model.ParseTagFilter(longKey + op + "abc1000")
		wantErr(t, err, model.KindBadRequest, "Value cannot be parsed as long.")
	}
}

// sorting: the metadata set is the one of FilterPagingAndNestedSortingIT.getSort*TestMetadata
var (
	idA = "d6eff51a-a231-4605-92ee-4ee453108e2a"
	idB = "8de2989e-14fa-4481-85c1-70e2a7ed1b19"
	idC = "47ef38bb-7130-487f-8fa5-748ebb765f8b"
	idN = "c0a371e5-da1d-4de7-a748-fd7b1a5ac51f" // without the tag
)

func seedSort(e *env, key string, vt model.TagValueType, a, b, c string) {
	mk := func(v string) model.Tag { return model.Tag{Key: key, Value: v, ValueType: vt} }
	// persisted in the Java order: c, a, (none), b
	e.persist(idC, mk(c))
	e.persist(idA, mk(a))
	e.persist(idN)
	e.persist(idB, mk(b))
}

func TestSortByLongTagValue(t *testing.T) {
	// 13 (A) < 479 (B) < 789 (C); secrets without the tag: NULL, last in ASC and first in DESC (PostgreSQL)
	for _, tc := range []struct {
		desc bool
		want []string
	}{
		{false, []string{idA, idB, idC, idN}},
		{true, []string{idN, idC, idB, idA}},
	} {
		e := newEnv(t)
		seedSort(e, "sk", model.TypeLong, "13", "479", "789")
		got := listIDs(t, e, pgstore.ListParams{SortKey: "sk", SortDesc: tc.desc})
		if fmt.Sprint(got) != fmt.Sprint(tc.want) {
			t.Fatalf("desc=%v: got %v want %v", tc.desc, got, tc.want)
		}
	}
}

func TestSortByStringTagValue(t *testing.T) {
	// values: C "dBxhR", A "bFj4g", B "dbxhR". Order of the Java expectation (database collation of
	// postgres:11.1, en_US.UTF-8): bFj4g, dbxhR, dBxhR, then NULL.
	for _, tc := range []struct {
		desc bool
		want []string
	}{
		{false, []string{idA, idB, idC, idN}},
		{true, []string{idN, idC, idB, idA}},
	} {
		e := newEnv(t)
		seedSort(e, "sk", model.TypeString, "bFj4g", "dbxhR", "dBxhR")
		got := listIDs(t, e, pgstore.ListParams{SortKey: "sk", SortDesc: tc.desc})
		if fmt.Sprint(got) != fmt.Sprint(tc.want) {
			t.Fatalf("desc=%v: got %v want %v", tc.desc, got, tc.want)
		}
	}
}

// givenMultipleEntitiesButMixedValueTypes_whenRequestingSortedList_thenThrowException
func TestSortWithMixedValueTypes(t *testing.T) {
	e := newEnv(t)
	e.persist(testSecretID, model.Tag{Key: "sk", Value: "123", ValueType: model.TypeLong})
	e.persist(testSecretID2, model.Tag{Key: "sk", Value: "textValue", ValueType: model.TypeString})
	e.persist(testSecretID3)
	_, err := e.svc.List(e.ctx(), pgstore.ListParams{SortKey: "sk"})
	wantErr(t, err, model.KindInternal, "The TagValueType of the tags used for sorting have different configurations.")
}

// givenNoTagWithSortingKey_whenRequestingSortedList_thenActAsNoSortingApplied
func TestSortByUnknownKeyActsUnsorted(t *testing.T) {
	e := newEnv(t)
	seedSort(e, "actual", model.TypeString, "bFj4g", "dbxhR", "dBxhR")
	got := listIDs(t, e, pgstore.ListParams{SortKey: "wrongKey"})
	if len(got) != 4 {
		t.Fatalf("%v", got)
	}
	// insertion order (the Java test only asserts the relative order of an unsorted list)
	if fmt.Sprint(got) != fmt.Sprint([]string{idC, idA, idN, idB}) {
		t.Fatalf("unsorted result must keep the storage order, got %v", got)
	}
}

// StorageIT givenSuccessfulRequest_whenGetObjectList_thenReturnExpectedResult
func TestListSingleSecretWithUnsortedConfig(t *testing.T) {
	e := newEnv(t)
	e.persist(testSecretID, testTag)
	p, err := e.svc.List(e.ctx(), pgstore.ListParams{})
	if err != nil {
		t.Fatal(err)
	}
	sameMetadata(t, p.Content, []model.Metadata{{SecretID: testSecretID, Tags: []model.Tag{testTag}}})
}

// filters, sorting and paging combine; the page metadata follows Spring's PageImpl
func TestFilterSortAndPagingCombined(t *testing.T) {
	e := newEnv(t)
	for i := 1; i <= 7; i++ {
		tags := []model.Tag{{Key: "rank", Value: fmt.Sprint(i * 10), ValueType: model.TypeLong}}
		if i%2 == 0 {
			tags = append(tags, model.Tag{Key: "even", Value: "yes", ValueType: model.TypeString})
		}
		e.persist(model.UUIDFromInt(int64(i)), tags...)
	}
	p, err := e.svc.List(e.ctx(), pgstore.ListParams{
		Filters: []model.TagFilter{{Key: "even", Value: "yes", Op: model.OpEquals}, {Key: "rank", Value: "20", Op: model.OpGreaterThan}},
		SortKey: "rank", SortDesc: true, Paged: true, Page: 0, PageSize: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	// rank > 20 and even: 40, 60 -> descending: 60 (id 6) first
	if fmt.Sprint(idsOf(p)) != fmt.Sprint([]string{model.UUIDFromInt(6)}) || p.TotalElements != 2 || p.TotalPages != 2 || p.Size != 1 || p.Number != 0 {
		t.Fatalf("%+v", p)
	}
}
