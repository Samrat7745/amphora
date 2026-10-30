package model

// Ports of the amphora-common entity tests (MaskedInput, Metadata, OutputDeliveryObject,
// SecretShare, TagFilter, Tag, VerifiableSecret) to the Go server model.

import (
	"encoding/base64"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

const entSecretID = "80fbba1b-3da8-4b1e-8a2c-cebd65229fad"

func b64s(n int, fill byte) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = fill
	}
	return base64.StdEncoding.EncodeToString(b)
}

// ---- TagTest ----

func TestTagBuilding(t *testing.T) {
	const dummyKey, dummyValue = "dummy-key", "dummy-value"
	t.Run("givenKeyWithTooManyCharacters_thenReject", func(t *testing.T) {
		_, err := NewTag(strings.Repeat("a", 129), dummyValue, TypeString)
		if err == nil || !strings.Contains(err.Error(), "longer then 128 characters") {
			t.Fatal(err)
		}
	})
	t.Run("givenKeyWithMaximumLength_thenSucceed", func(t *testing.T) {
		if _, err := NewTag(strings.Repeat("a", 128), dummyValue, TypeString); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("givenKeyWithInvalidCharacter_thenReject", func(t *testing.T) {
		if _, err := NewTag("#123", dummyValue, TypeString); err == nil || err.Error() != InvalidKeyStringMsg {
			t.Fatal(err)
		}
	})
	t.Run("giveKeyIsEmptyString_thenReject", func(t *testing.T) {
		if _, err := NewTag("", dummyValue, TypeString); err == nil || err.Error() != InvalidKeyStringMsg {
			t.Fatal(err)
		}
	})
	t.Run("givenNullAsKeyOrValue_thenReject", func(t *testing.T) {
		// Lombok @NonNull NPE in Java; a null/missing JSON member is a binding error (400, empty body).
		for _, body := range []string{`{"value":"v"}`, `{"key":null}`, `{"key":"k","value":null}`} {
			_, err := ParseTag([]byte(body))
			if e, ok := err.(*Error); !ok || e.Kind != KindBinding {
				t.Errorf("%s: %v", body, err)
			}
		}
	})
	t.Run("givenValueIsEmptyString_thenCreateExpectedTag", func(t *testing.T) {
		tag, err := NewTag(dummyKey, "", TypeString)
		if err != nil || tag != (Tag{dummyKey, "", TypeString}) {
			t.Fatal(tag, err)
		}
	})
	t.Run("givenKeyWithSupportedCharacters_thenSucceed", func(t *testing.T) {
		tag, err := NewTag("abcDEF456-.", dummyValue, TypeString)
		if err != nil || tag.Key != "abcDEF456-." || tag.ValueType != TypeString || tag.Value != dummyValue {
			t.Fatal(tag, err)
		}
	})
	t.Run("givenValueWithTooManyCharacters_thenReject", func(t *testing.T) {
		_, err := NewTag(dummyKey, strings.Repeat("a", 257), TypeString)
		if err == nil || !strings.HasPrefix(err.Error(), "The given value exceeds the maximum length of 256: (257)") {
			t.Fatal(err)
		}
	})
	t.Run("givenValueWithMaximumLength_thenSucceed", func(t *testing.T) {
		v := strings.Repeat("a", 256)
		tag, err := NewTag(dummyKey, v, TypeString)
		if err != nil || tag.Value != v {
			t.Fatal(err)
		}
	})
	t.Run("givenLongStringAsValueAndLongAsValueType_thenCreateExpectedTag", func(t *testing.T) {
		tag, err := NewTag(dummyKey, "12345", TypeLong)
		if err != nil || tag != (Tag{dummyKey, "12345", TypeLong}) {
			t.Fatal(tag, err)
		}
		if _, err := NewTag(dummyKey, "12345a", TypeLong); err == nil || err.Error() != "Value cannot be parsed as long." {
			t.Fatal(err)
		}
	})
	t.Run("givenOnlyKeyAsInput_thenUseDefaultValues", func(t *testing.T) {
		tag, err := ParseTag([]byte(`{"key":"dummy-key"}`))
		if err != nil || tag != (Tag{dummyKey, "", TypeString}) {
			t.Fatal(tag, err)
		}
	})
	t.Run("givenTagObject_whenSerializingAndDeserializing_thenRecreateOrigin", func(t *testing.T) {
		tag := Tag{dummyKey, "12345", TypeLong}
		got, err := ParseTag([]byte(tag.JSON()))
		if err != nil || got != tag {
			t.Fatal(got, err)
		}
		if tag.JSON() != `{"key":"dummy-key","value":"12345","valueType":"LONG"}` {
			t.Fatal(tag.JSON())
		}
	})
}

// ---- TagFilterTest ----

func TestTagFilterFromString(t *testing.T) {
	for name, in := range map[string]string{
		"givenTagFilterStringWithoutKey":          ":42",
		"givenTagFilterStringWithInvalidOperator": "key<>Value",
		"givenTagFilterStringWithoutValue":        "key:",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseTagFilter(in)
			e, ok := err.(*Error)
			if !ok || e.Kind != KindBadRequest || e.Msg != fmt.Sprintf(`The given String cannot be parsed as a TagFilter: "%s".`, in) {
				t.Fatal(err)
			}
		})
	}
	t.Run("givenValidTagFilterString_thenReturnExpectedTagFilter", func(t *testing.T) {
		got, err := ParseTagFilter("time<42")
		if err != nil || got != (TagFilter{Key: "time", Value: "42", Op: OpLessThan}) {
			t.Fatal(got, err)
		}
		for in, op := range map[string]TagFilterOperator{"k:v": OpEquals, "k>1": OpGreaterThan} {
			f, err := ParseTagFilter(in)
			if err != nil || f.Op != op {
				t.Errorf("%s: %v %v", in, f, err)
			}
		}
	})
}

// ---- SecretShareTest / MetadataTest ----

func TestSecretShareParsing(t *testing.T) {
	t.Run("givenDataOfInvalidLength_thenReject", func(t *testing.T) {
		_, err := ParseSecretShare([]byte(`{"secretId":"` + entSecretID + `","data":"` + b64s(SecretShareWidth-1, 0) + `"}`))
		if e, ok := err.(*Error); !ok || e.Kind != KindBinding {
			t.Fatal(err)
		}
	})
	t.Run("givenDataOfVariableButValidLength_thenSucceed", func(t *testing.T) {
		for i := 0; i < 100; i++ {
			raw := make([]byte, SecretShareWidth*i)
			for j := range raw {
				raw[j] = byte(j*7 + i)
			}
			s, err := ParseSecretShare([]byte(`{"secretId":"` + entSecretID + `","data":"` + base64.StdEncoding.EncodeToString(raw) + `","tags":[]}`))
			if err != nil || !reflect.DeepEqual(append([]byte{}, s.Data...), append([]byte{}, raw...)) {
				t.Fatalf("%d words: %v", i, err)
			}
		}
	})
	t.Run("givenSecretShareObject_whenSerialize_thenReturnExpectedJsonString", func(t *testing.T) {
		data := make([]byte, SecretShareWidth)
		for i := range data {
			data[i] = byte(i)
		}
		tags := []Tag{{"theKey", "someValue", TypeString}}
		s := SecretShare{Metadata: Metadata{SecretID: entSecretID, Tags: tags}, Data: data}
		want := fmt.Sprintf(`{"secretId":"%s","tags":%s,"data":"%s"}`, entSecretID, TagsJSON(tags), base64.StdEncoding.EncodeToString(data))
		if s.JSON() != want || TagsJSON(tags) != `[{"key":"theKey","value":"someValue","valueType":"STRING"}]` {
			t.Fatal(s.JSON())
		}
	})
	t.Run("givenValidSecretShareJsonString_whenDeserialize_thenReturnExpectedObject", func(t *testing.T) {
		data := make([]byte, SecretShareWidth)
		for i := range data {
			data[i] = byte(255 - i)
		}
		in := SecretShare{Metadata: Metadata{SecretID: entSecretID, Tags: []Tag{{"theKey", "someValue", TypeString}}}, Data: data}
		got, err := ParseSecretShare([]byte(in.JSON()))
		if err != nil || !reflect.DeepEqual(*got, in) {
			t.Fatalf("%+v %v", got, err)
		}
	})
	t.Run("givenTwoSecretSharesWithSameContent_whenCompareEqual_thenMatch", func(t *testing.T) {
		body := []byte(`{"secretId":"` + entSecretID + `","data":"` + b64s(32, 3) + `","tags":[]}`)
		a, _ := ParseSecretShare(body)
		b, _ := ParseSecretShare(body)
		if !reflect.DeepEqual(a, b) {
			t.Fatal("must be equal")
		}
	})
}

func TestMetadata(t *testing.T) {
	t.Run("givenIdIsNull_thenReject", func(t *testing.T) {
		for _, body := range []string{`{"data":"` + b64s(32, 0) + `"}`, `{"secretId":null,"data":"` + b64s(32, 0) + `"}`} {
			if _, err := ParseSecretShare([]byte(body)); err == nil {
				t.Errorf("%s must be rejected", body)
			}
		}
	})
	t.Run("givenListWithNullTags_thenRemoveNullTagsFromList", func(t *testing.T) {
		s, err := ParseSecretShare([]byte(`{"secretId":"` + entSecretID + `","data":"` + b64s(32, 0) + `","tags":[null,{"key":"key1"},{"key":"key2"},null]}`))
		if err != nil {
			t.Fatal(err)
		}
		want := []Tag{{"key1", "", TypeString}, {"key2", "", TypeString}}
		if !reflect.DeepEqual(s.Tags, want) {
			t.Fatalf("%+v", s.Tags)
		}
	})
}

// ---- MaskedInputTest / MaskedInputDataTest ----

func TestMaskedInputEntity(t *testing.T) {
	t.Run("givenIdIsNull_thenReject", func(t *testing.T) {
		for _, body := range []string{`{"data":[],"tags":[]}`, `{"secretId":null,"data":[],"tags":[]}`} {
			if _, err := ParseMaskedInput([]byte(body)); err == nil {
				t.Errorf("%s must be rejected", body)
			}
		}
	})
	t.Run("givenTagsAreNull_thenUseEmptyListInstead", func(t *testing.T) {
		for _, body := range []string{
			`{"secretId":"` + entSecretID + `","data":[{"value":"` + b64s(16, 0) + `"}]}`,
			`{"secretId":"` + entSecretID + `","data":[{"value":"` + b64s(16, 0) + `"}],"tags":null}`,
			`{"secretId":"` + entSecretID + `","data":[{"value":"` + b64s(16, 0) + `"}],"tags":[]}`,
		} {
			mi, err := ParseMaskedInput([]byte(body))
			if err != nil || mi.SecretID != entSecretID || mi.Tags == nil || len(mi.Tags) != 0 || len(mi.Data) != 1 {
				t.Fatalf("%s: %+v %v", body, mi, err)
			}
		}
	})
	t.Run("masked input data values keep their raw length (validated when converted, not when parsed)", func(t *testing.T) {
		// Java deserialization bypasses MaskedInputData.of; the 16 byte rule surfaces in SecretShareUtil
		// (see service TestConvertToSecretShareRejectsWrongLength).
		mi, err := ParseMaskedInput([]byte(`{"secretId":"` + entSecretID + `","data":[{"value":"` + b64s(15, 0) + `"}]}`))
		if err != nil || len(mi.Data[0]) != 15 {
			t.Fatal(mi, err)
		}
	})
}

// ---- OutputDeliveryObjectTest / VerifiableSecretTest ----

func TestOutputDeliveryObjectJSON(t *testing.T) {
	b := func(s string) []byte { return []byte(s) }
	odo := OutputDeliveryObject{SecretShares: b("sShares"), RShares: b("rShares"), VShares: b("vShares"), WShares: b("wShares"), UShares: b("uShares")}
	e := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	want := fmt.Sprintf(`{"secretShares":"%s","rShares":"%s","vShares":"%s","wShares":"%s","uShares":"%s"}`,
		e("sShares"), e("rShares"), e("vShares"), e("wShares"), e("uShares"))
	if odo.JSON() != want {
		t.Fatal(odo.JSON())
	}
}

func TestVerifiableSecretShareJSON(t *testing.T) {
	// Java VerifiableSecretShare serializer: flat merge of the secret share and the output delivery object.
	b := func(s string) []byte { return []byte(s) }
	odo := OutputDeliveryObject{SecretShares: b("sShares"), RShares: b("rShares"), VShares: b("vShares"), WShares: b("wShares"), UShares: b("uShares")}
	share := SecretShare{Metadata: Metadata{SecretID: entSecretID, Tags: []Tag{{"carbyne", "stack", TypeString}}}, Data: b("data")}
	e := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	want := fmt.Sprintf(`{"secretId":"%s","tags":[{"key":"carbyne","value":"stack","valueType":"STRING"}],"data":"%s",`+
		`"secretShares":"%s","rShares":"%s","vShares":"%s","wShares":"%s","uShares":"%s"}`,
		entSecretID, e("data"), e("sShares"), e("rShares"), e("vShares"), e("wShares"), e("uShares"))
	if got := VerifiableSecretShareJSON(share, odo); got != want {
		t.Fatalf("\n got %s\nwant %s", got, want)
	}
}
