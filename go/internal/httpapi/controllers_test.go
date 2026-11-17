package httpapi

// Ports of the Java controller unit tests (rest/*ControllerTest). The Java tests call controller
// methods directly with mocked services; here the same inputs go through HTTP into a fake Backend
// and the assertions cover the status code, body and the arguments handed to the backend.

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/Samrat7745/amphora/go/internal/jwt"
	"github.com/Samrat7745/amphora/go/internal/model"
	"github.com/Samrat7745/amphora/go/internal/pgstore"
)

const (
	authorizedUserID = "afc0117f-c9cd-4d8c-acee-fa1433ca0fdd"
	testSecretID     = "3bcf8308-8f50-4d24-a37b-b0075bb5e779"
	testRequestID    = "d6d0f4ff-df28-4c96-b7df-95170320eaee"
)

var authHeaderValue = "Bearer h." + base64.StdEncoding.EncodeToString([]byte(`{"sub":"`+authorizedUserID+`"}`)) + ".s"

// fakeBackend records the calls and returns the configured results.
type fakeBackend struct {
	calls []string
	args  []any

	listPage   model.MetadataPage
	secret     *model.SecretShare
	tags       []model.Tag
	tag        model.Tag
	createdID  string
	odo        model.OutputDeliveryObject
	storeShare string
	useErr     error // returned by UseSecretShare
}

func (f *fakeBackend) rec(name string, a ...any) {
	f.calls = append(f.calls, name)
	f.args = append(f.args, a)
}

func (f *fakeBackend) List(_ context.Context, p pgstore.ListParams) (model.MetadataPage, error) {
	f.rec("List", p)
	return f.listPage, nil
}
func (f *fakeBackend) GetSecretShare(_ context.Context, id, user string) (*model.SecretShare, error) {
	f.rec("GetSecretShare", id, user)
	return f.secret, nil
}
func (f *fakeBackend) DeleteSecret(_ context.Context, id, user string) error {
	f.rec("DeleteSecret", id, user)
	return nil
}
func (f *fakeBackend) RetrieveTags(_ context.Context, id, user string) ([]model.Tag, error) {
	f.rec("RetrieveTags", id, user)
	return f.tags, nil
}
func (f *fakeBackend) StoreTag(_ context.Context, id string, t model.Tag, user string) (string, error) {
	f.rec("StoreTag", id, t, user)
	return t.Key, nil
}
func (f *fakeBackend) ReplaceTags(_ context.Context, id string, t []model.Tag, user string) error {
	f.rec("ReplaceTags", id, t, user)
	return nil
}
func (f *fakeBackend) RetrieveTag(_ context.Context, id, key, user string) (model.Tag, error) {
	f.rec("RetrieveTag", id, key, user)
	return f.tag, nil
}
func (f *fakeBackend) UpdateTag(_ context.Context, id string, t model.Tag, user string) error {
	f.rec("UpdateTag", id, t, user)
	return nil
}
func (f *fakeBackend) DeleteTag(_ context.Context, id, key, user string) error {
	f.rec("DeleteTag", id, key, user)
	return nil
}
func (f *fakeBackend) CreateSecret(_ context.Context, mi *model.MaskedInput, user string) (string, error) {
	f.rec("CreateSecret", mi, user)
	return mi.SecretID, nil
}
func (f *fakeBackend) InputMasks(_ context.Context, rid string, count int64) (model.OutputDeliveryObject, error) {
	f.rec("InputMasks", rid, count)
	return f.odo, nil
}
func (f *fakeBackend) StoreSecretShare(_ context.Context, s *model.SecretShare) (string, error) {
	f.rec("StoreSecretShare", s)
	return s.SecretID, nil
}
func (f *fakeBackend) UseSecretShare(_ context.Context, id, program string) (*model.SecretShare, error) {
	f.rec("UseSecretShare", id, program)
	if f.useErr != nil {
		return nil, f.useErr
	}
	return f.secret, nil
}
func (f *fakeBackend) PutInterim(_ context.Context, e model.ExchangeObject) error {
	f.rec("PutInterim", e)
	return nil
}

type fakeODO struct {
	odo  model.OutputDeliveryObject
	data []byte
	rid  string
}

func (f *fakeODO) ComputeFromSecretShareData(_ context.Context, data []byte, rid string) (model.OutputDeliveryObject, error) {
	f.data, f.rid = data, rid
	return f.odo, nil
}

type resp struct {
	code int
	body string
	hdr  http.Header
}

func newAPI(be Backend, o OutputDelivery) http.Handler {
	a := &API{Svc: be, ODO: o, JWT: jwt.Reader{Field: "sub"}}
	return a.Handler()
}

func call(h http.Handler, method, target, body string, hdr map[string]string) resp {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	r := httptest.NewRequest(method, target, rd)
	r.Host = "amphora.carbynestack.io"
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return resp{w.Code, w.Body.String(), w.Header()}
}

func authed() map[string]string { return map[string]string{"Authorization": authHeaderValue} }

func (f *fakeBackend) called(name string) bool {
	for _, c := range f.calls {
		if c == name {
			return true
		}
	}
	return false
}

func (f *fakeBackend) argsOf(name string) []any {
	for i, c := range f.calls {
		if c == name {
			return f.args[i].([]any)
		}
	}
	return nil
}

func wantResp(t *testing.T, r resp, code int, body string) {
	t.Helper()
	if r.code != code || (body != "*" && r.body != body) {
		t.Fatalf("got %d %q, want %d %q", r.code, r.body, code, body)
	}
}

func sampleODO() model.OutputDeliveryObject {
	b := func(s string) []byte { return []byte(s) }
	return model.OutputDeliveryObject{SecretShares: b("sShares"), RShares: b("rShares"), VShares: b("vShares"), WShares: b("wShares"), UShares: b("uShares")}
}

// ---- InputMaskShareControllerTest ----

func TestInputMasksController(t *testing.T) {
	const rid = "7520e090-1437-44da-9e4e-eea5b2200fea"
	t.Run("givenSuccessfulRequest_whenGetInputMasks_thenReturnExpectedResult", func(t *testing.T) {
		f := &fakeBackend{odo: sampleODO()}
		r := call(newAPI(f, nil), "GET", "/input-masks?requestId="+rid+"&count=1", "", nil)
		wantResp(t, r, 200, sampleODO().JSON())
		if got := f.argsOf("InputMasks"); !reflect.DeepEqual(got, []any{rid, int64(1)}) {
			t.Fatal(got)
		}
	})
	t.Run("givenMissingOrEmptyRequestId_whenGetInputMasks_thenReject", func(t *testing.T) {
		// Java: IllegalArgumentException REQUEST_IDENTIFIER_MUST_NOT_BE_NULL (controller called with null);
		// over HTTP Spring rejects the missing parameter with an empty 400.
		for _, target := range []string{"/input-masks?count=1", "/input-masks?requestId=&count=1", "/input-masks?requestId=nope&count=1"} {
			f := &fakeBackend{}
			wantResp(t, call(newAPI(f, nil), "GET", target, "", nil), 400, "")
			if f.called("InputMasks") {
				t.Fatal("backend must not be called")
			}
		}
	})
	t.Run("givenInvalidCountArgument_whenGetInputMasks_thenBadRequestWithMessage", func(t *testing.T) {
		for _, c := range []string{"-1", "0"} {
			f := &fakeBackend{}
			r := call(newAPI(f, nil), "GET", "/input-masks?requestId="+rid+"&count="+c, "", nil)
			wantResp(t, r, 400, `"The number of requested Input Masks has to be 1 or greater."`)
			if f.called("InputMasks") {
				t.Fatal("backend must not be called")
			}
		}
	})
}

// ---- InterVcpControllerTest ----

func TestInterVcpController(t *testing.T) {
	f := &fakeBackend{}
	body := `{"operationId":"ea983b9b-0e98-4cbb-8dbf-3c8362653c0d","playerId":0,"interimValues":[]}`
	r := call(newAPI(f, nil), "POST", "/inter-vcp/open", body, nil)
	wantResp(t, r, http.StatusAccepted, "")
	want := model.ExchangeObject{OperationID: "ea983b9b-0e98-4cbb-8dbf-3c8362653c0d", PlayerID: 0, Values: []model.FactorPair{}}
	got := f.argsOf("PutInterim")
	if len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Fatalf("%#v", got)
	}
	if n := len(f.calls); n != 1 {
		t.Fatalf("putInterimValues must be called exactly once, got %d", n)
	}
}

// ---- IntraVcpControllerTest ----

func TestIntraVcpController(t *testing.T) {
	data := base64.StdEncoding.EncodeToString(make([]byte, 32))
	t.Run("givenArgumentIsNull_whenUploadSecretShare_thenReject", func(t *testing.T) {
		f := &fakeBackend{}
		wantResp(t, call(newAPI(f, nil), "POST", "/intra-vcp/secret-shares", "null", nil), 400, "")
		if f.called("StoreSecretShare") {
			t.Fatal("backend must not be called")
		}
	})
	t.Run("givenSuccessfulRequest_whenUploadSecretShare_thenReturnCreatedWithExpectedContent", func(t *testing.T) {
		f := &fakeBackend{}
		r := call(newAPI(f, nil), "POST", "/intra-vcp/secret-shares", `{"secretId":"`+testSecretID+`","data":"`+data+`"}`, nil)
		wantResp(t, r, 201, `"http://amphora.carbynestack.io/intra-vcp/secret-shares/`+testSecretID+`"`)
		if ct := r.hdr.Get("Content-Type"); ct != "application/json" {
			t.Fatal(ct)
		}
		s := f.argsOf("StoreSecretShare")[0].(*model.SecretShare)
		if s.SecretID != testSecretID || len(s.Data) != 32 {
			t.Fatalf("%+v", s)
		}
	})
	t.Run("givenSuccessfulRequest_whenDownloadSecretShare_thenReturnOkWithExpectedContent", func(t *testing.T) {
		share := &model.SecretShare{Metadata: model.Metadata{SecretID: testSecretID, Tags: []model.Tag{}}, Data: make([]byte, 32)}
		f := &fakeBackend{secret: share}
		r := call(newAPI(f, nil), "GET", "/intra-vcp/secret-shares/"+testSecretID+"?programId="+authorizedUserID, "", nil)
		wantResp(t, r, 200, share.JSON())
		if got := f.argsOf("UseSecretShare"); !reflect.DeepEqual(got, []any{testSecretID, authorizedUserID}) {
			t.Fatal(got)
		}
	})
	t.Run("givenMissingProgramId_whenDownloadSecretShare_thenReject", func(t *testing.T) {
		f := &fakeBackend{}
		wantResp(t, call(newAPI(f, nil), "GET", "/intra-vcp/secret-shares/"+testSecretID, "", nil), 400, "")
		if f.called("UseSecretShare") {
			t.Fatal("backend must not be called")
		}
	})
	// Java: @RequestParam String binds "" (only a missing parameter is a binding error); the empty program id
	// reaches the OPA check, which throws "Subject is required to evaluate the policy" -> 500 (fixture
	// e13_intra_get_empty_programid, recorded from the original Java 8 image).
	t.Run("givenEmptyProgramId_whenDownloadSecretShare_thenPassOnLikeJava", func(t *testing.T) {
		f := &fakeBackend{useErr: model.Internal("Subject is required to evaluate the policy")}
		wantResp(t, call(newAPI(f, nil), "GET", "/intra-vcp/secret-shares/"+testSecretID+"?programId=", "", nil), 500, `"Subject is required to evaluate the policy"`)
		if got := f.argsOf("UseSecretShare"); !reflect.DeepEqual(got, []any{testSecretID, ""}) {
			t.Fatal(got)
		}
	})
}

// ---- MaskedInputControllerTest ----

func TestMaskedInputController(t *testing.T) {
	zero16 := base64.StdEncoding.EncodeToString(make([]byte, 16))
	t.Run("givenArgumentIsNull_whenUpload_thenReject", func(t *testing.T) {
		f := &fakeBackend{}
		wantResp(t, call(newAPI(f, nil), "POST", "/masked-inputs", "null", authed()), 400, "")
		if f.called("CreateSecret") {
			t.Fatal("backend must not be called")
		}
	})
	t.Run("givenMaskedInputDataIsEmpty_whenUpload_thenBadRequestWithMessage", func(t *testing.T) {
		f := &fakeBackend{}
		r := call(newAPI(f, nil), "POST", "/masked-inputs", `{"secretId":"`+testSecretID+`","data":[],"tags":[]}`, authed())
		wantResp(t, r, 400, `"MaskedInput data must not be empty"`)
		if f.called("CreateSecret") {
			t.Fatal("backend must not be called")
		}
	})
	t.Run("givenSuccessfulRequest_whenUpload_thenReturnCreatedWithExpectedContent", func(t *testing.T) {
		f := &fakeBackend{}
		r := call(newAPI(f, nil), "POST", "/masked-inputs", `{"secretId":"`+testSecretID+`","data":[{"value":"`+zero16+`"}],"tags":[]}`, authed())
		wantResp(t, r, 201, `"http://amphora.carbynestack.io/masked-inputs/`+testSecretID+`"`)
		got := f.argsOf("CreateSecret")
		mi := got[0].(*model.MaskedInput)
		if mi.SecretID != testSecretID || len(mi.Data) != 1 || len(mi.Data[0]) != 16 || got[1] != authorizedUserID {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("authorization header is required", func(t *testing.T) {
		f := &fakeBackend{}
		wantResp(t, call(newAPI(f, nil), "POST", "/masked-inputs", `{"secretId":"`+testSecretID+`","data":[{"value":"`+zero16+`"}]}`, nil), 400, "")
	})
}

// ---- SecretShareControllerTest ----

func emptyPage() model.MetadataPage {
	return model.MetadataPage{Content: []model.Metadata{}, Number: 0, Size: 0, TotalElements: 0, TotalPages: 1}
}

func TestListController(t *testing.T) {
	filter := "key:value,key2<42"
	wantFilters := []model.TagFilter{{Key: "key", Value: "value", Op: model.OpEquals}, {Key: "key2", Value: "42", Op: model.OpLessThan}}
	cases := []struct {
		name  string
		query string
		want  pgstore.ListParams
	}{
		{"withoutFilterAndWithoutPaging", "", pgstore.ListParams{}},
		{"explicitZeroPaging", "?pageNumber=0&pageSize=0", pgstore.ListParams{}},
		{"withoutFilterButWithPaging", "?pageNumber=0&pageSize=1", pgstore.ListParams{Paged: true, Page: 0, PageSize: 1}},
		{"withFilterAndWithoutPaging", "?filter=" + filter, pgstore.ListParams{Filters: wantFilters}},
		{"withFilterButWithPaging", "?filter=" + filter + "&pageNumber=0&pageSize=1", pgstore.ListParams{Filters: wantFilters, Paged: true, Page: 0, PageSize: 1}},
		// getSort tests
		{"sortPropertyButInvalidDirection_thenAsc", "?sort=key&dir=invalid", pgstore.ListParams{SortKey: "key"}},
		{"noSortProperty_thenUnsorted", "?sort=&dir=ASC", pgstore.ListParams{}},
		{"validSortConfiguration_desc", "?sort=key&dir=DESC", pgstore.ListParams{SortKey: "key", SortDesc: true}},
		{"validSortConfiguration_ascUpper", "?sort=key&dir=ASC", pgstore.ListParams{SortKey: "key"}},
	}
	for _, c := range cases {
		t.Run("givenSuccessfulRequest_"+c.name, func(t *testing.T) {
			f := &fakeBackend{listPage: emptyPage()}
			r := call(newAPI(f, nil), "GET", "/secret-shares"+c.query, "", nil)
			wantResp(t, r, 200, `{"content":[],"number":0,"size":0,"totalElements":0,"totalPages":1}`)
			got := f.argsOf("List")[0].(pgstore.ListParams)
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %+v want %+v", got, c.want)
			}
		})
	}
}

func TestGetSecretShareController(t *testing.T) {
	t.Run("givenRequestIdArgumentIsNull_whenGetSecretShare_thenReject", func(t *testing.T) {
		f := &fakeBackend{}
		// Java: IAE "Request identifier must not be omitted" -> over HTTP a missing @RequestParam gives an empty 400.
		wantResp(t, call(newAPI(f, &fakeODO{}), "GET", "/secret-shares/"+testSecretID, "", authed()), 400, "")
		wantResp(t, call(newAPI(f, &fakeODO{}), "GET", "/secret-shares/"+testSecretID+"?requestId=", "", authed()), 400, "")
		if f.called("GetSecretShare") {
			t.Fatal("backend must not be called")
		}
	})
	t.Run("givenSuccessfulRequest_whenGetSecretShare_thenReturnOkAndExpectedContent", func(t *testing.T) {
		share := &model.SecretShare{Metadata: model.Metadata{SecretID: testRequestID, Tags: []model.Tag{}}, Data: make([]byte, 64)}
		f := &fakeBackend{secret: share}
		o := &fakeODO{odo: sampleODO()}
		r := call(newAPI(f, o), "GET", "/secret-shares/"+testSecretID+"?requestId="+testRequestID, "", authed())
		wantResp(t, r, 200, model.VerifiableSecretShareJSON(*share, sampleODO()))
		if got := f.argsOf("GetSecretShare"); !reflect.DeepEqual(got, []any{testSecretID, authorizedUserID}) {
			t.Fatal(got)
		}
		if o.rid != testRequestID {
			t.Fatal(o.rid)
		}
		// the ODO is computed from the value halves of the 32 byte shares
		if len(o.data) != 64 {
			t.Fatalf("ODO input %d bytes", len(o.data))
		}
	})
	t.Run("givenSuccessfulRequest_whenDeleteSecretShare_thenReturnOk", func(t *testing.T) {
		f := &fakeBackend{}
		wantResp(t, call(newAPI(f, nil), "DELETE", "/secret-shares/"+testSecretID, "", authed()), 200, "")
		if got := f.argsOf("DeleteSecret"); !reflect.DeepEqual(got, []any{testSecretID, authorizedUserID}) {
			t.Fatal(got)
		}
	})
}

// ---- TagsControllerTest ----

var testTag = model.Tag{Key: "key", Value: "value", ValueType: model.TypeString}

const testTagJSON = `{"key":"key","value":"value","valueType":"STRING"}`

func TestTagsController(t *testing.T) {
	base := "/secret-shares/" + testSecretID + "/tags"
	t.Run("givenSuccessfulRequest_whenGetTags_thenReturnOkWithExpectedContent", func(t *testing.T) {
		f := &fakeBackend{tags: []model.Tag{testTag}}
		wantResp(t, call(newAPI(f, nil), "GET", base, "", authed()), 200, "["+testTagJSON+"]")
		if got := f.argsOf("RetrieveTags"); !reflect.DeepEqual(got, []any{testSecretID, authorizedUserID}) {
			t.Fatal(got)
		}
	})
	t.Run("givenTagIsNull_whenCreateTag_thenReject", func(t *testing.T) {
		f := &fakeBackend{}
		wantResp(t, call(newAPI(f, nil), "POST", base, "null", authed()), 400, "")
		if f.called("StoreTag") {
			t.Fatal("storeTag must not be called")
		}
	})
	t.Run("givenSuccessfulRequest_whenCreateTag_thenReturnCreatedWithExpectedContent", func(t *testing.T) {
		f := &fakeBackend{}
		r := call(newAPI(f, nil), "POST", base, testTagJSON, authed())
		wantResp(t, r, 201, `"http://amphora.carbynestack.io`+base+`/key"`)
		if got := f.argsOf("StoreTag"); !reflect.DeepEqual(got, []any{testSecretID, testTag, authorizedUserID}) {
			t.Fatal(got)
		}
	})
	t.Run("givenTagsAreEmpty_whenUpdateTags_thenBadRequestWithMessage", func(t *testing.T) {
		f := &fakeBackend{}
		wantResp(t, call(newAPI(f, nil), "PUT", base, "[]", authed()), 400, `"At least one tag must be given."`)
		if f.called("ReplaceTags") {
			t.Fatal("replaceTags must not be called")
		}
	})
	t.Run("givenSuccessfulRequest_whenUpdateTags_thenReturnOk", func(t *testing.T) {
		f := &fakeBackend{}
		wantResp(t, call(newAPI(f, nil), "PUT", base, "["+testTagJSON+"]", authed()), 200, "")
		if got := f.argsOf("ReplaceTags"); !reflect.DeepEqual(got, []any{testSecretID, []model.Tag{testTag}, authorizedUserID}) {
			t.Fatal(got)
		}
	})
	t.Run("givenSuccessfulRequest_whenGetTag_thenReturnOkWithExpectedContent", func(t *testing.T) {
		f := &fakeBackend{tag: testTag}
		wantResp(t, call(newAPI(f, nil), "GET", base+"/key", "", authed()), 200, testTagJSON)
		if got := f.argsOf("RetrieveTag"); !reflect.DeepEqual(got, []any{testSecretID, "key", authorizedUserID}) {
			t.Fatal(got)
		}
	})
	t.Run("givenTagIsNull_whenPutTag_thenReject", func(t *testing.T) {
		f := &fakeBackend{}
		wantResp(t, call(newAPI(f, nil), "PUT", base+"/key", "null", authed()), 400, "")
		if f.called("UpdateTag") {
			t.Fatal("updateTag must not be called")
		}
	})
	t.Run("givenTagConfigurationDoesNotMatchAddressedKey_whenPutTag_thenBadRequestWithMessage", func(t *testing.T) {
		f := &fakeBackend{}
		r := call(newAPI(f, nil), "PUT", base+"/key_different", testTagJSON, authed())
		wantResp(t, r, 400, `"The defined key and tag data do not match.\nkey_different <> Tag(key=key, value=value, valueType=STRING)"`)
		if f.called("UpdateTag") {
			t.Fatal("updateTag must not be called")
		}
	})
	t.Run("givenSuccessfulRequest_whenPutTag_thenReturnOk", func(t *testing.T) {
		f := &fakeBackend{}
		wantResp(t, call(newAPI(f, nil), "PUT", base+"/key", testTagJSON, authed()), 200, "")
		if got := f.argsOf("UpdateTag"); !reflect.DeepEqual(got, []any{testSecretID, testTag, authorizedUserID}) {
			t.Fatal(got)
		}
	})
	t.Run("givenSuccessfulRequest_whenDeleteTag_thenReturnOk", func(t *testing.T) {
		f := &fakeBackend{}
		wantResp(t, call(newAPI(f, nil), "DELETE", base+"/key", "", authed()), 200, "")
		if got := f.argsOf("DeleteTag"); !reflect.DeepEqual(got, []any{testSecretID, "key", authorizedUserID}) {
			t.Fatal(got)
		}
	})
}

// Authentication errors surface as 401 "No token provided" for every authenticated endpoint.
func TestMissingTokenIsUnauthorized(t *testing.T) {
	f := &fakeBackend{}
	h := newAPI(f, &fakeODO{})
	bad := map[string]string{"Authorization": "invalid_auth_header missing Bearer prefix"}
	for _, c := range []struct{ method, target, body string }{
		{"GET", "/secret-shares/" + testSecretID + "?requestId=" + testRequestID, ""},
		{"DELETE", "/secret-shares/" + testSecretID, ""},
		{"GET", "/secret-shares/" + testSecretID + "/tags", ""},
		{"POST", "/secret-shares/" + testSecretID + "/tags", testTagJSON},
		{"GET", "/secret-shares/" + testSecretID + "/tags/key", ""},
	} {
		wantResp(t, call(h, c.method, c.target, c.body, bad), 401, `"No token provided"`)
	}
	if len(f.calls) != 0 {
		t.Fatalf("backend called: %v", f.calls)
	}
}
