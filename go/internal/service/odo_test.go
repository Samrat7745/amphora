package service

// Ports of OutputDeliveryServiceTest and SecretShareUtilTest.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/Samrat7745/amphora/go/internal/castor"
	"github.com/Samrat7745/amphora/go/internal/javaser"
	"github.com/Samrat7745/amphora/go/internal/model"
	"github.com/Samrat7745/amphora/go/spdz"
)

func testSpdz() *spdz.Utils {
	p, _ := new(big.Int).SetString("198766463529478683931867765928436695041", 10)
	r, _ := new(big.Int).SetString("141515903391459779531506841503331516415", 10)
	ri, _ := new(big.Int).SetString("133854242216446749056083838363708373830", 10)
	return spdz.New(p, r, ri)
}

func bigs(v ...int64) []*big.Int {
	out := make([]*big.Int, len(v))
	for i, x := range v {
		out[i] = big.NewInt(x)
	}
	return out
}

func gfpBytes(t *testing.T, sp *spdz.Utils, vals ...*big.Int) []byte {
	t.Helper()
	var out []byte
	for _, v := range vals {
		b, err := sp.ToGfp(v)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, b...)
	}
	return out
}

// ---- fakes ----

type castorCall struct {
	reqID, tupleType string
	count            int64
}

type fakeCastor struct {
	mu     sync.Mutex
	calls  []castorCall
	tuples map[string][]castor.Tuple // keyed by reqID|type
	err    map[string]error
}

func castorKey(reqID, typ string) string { return reqID + "|" + typ }

func (f *fakeCastor) Download(_ context.Context, reqID, typ string, count int64) ([]castor.Tuple, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, castorCall{reqID, typ, count})
	if e := f.err[castorKey(reqID, typ)]; e != nil {
		return nil, e
	}
	return f.tuples[castorKey(reqID, typ)], nil
}

type fakePeers struct {
	mu     sync.Mutex
	opened []model.ExchangeObject
	err    error
}

func (f *fakePeers) Open(_ context.Context, e model.ExchangeObject) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opened = append(f.opened, e)
	return f.err
}

type fakeInterim struct {
	mu      sync.Mutex
	put     []model.ExchangeObject
	values  map[int32][]model.FactorPair // nil entry: not (yet) available
	getCall map[int32]int
}

func (f *fakeInterim) PutInterim(_ context.Context, e model.ExchangeObject) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.put = append(f.put, e)
	return nil
}

func (f *fakeInterim) GetAndEvictInterim(_ context.Context, _ string, player int32) ([]model.FactorPair, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getCall == nil {
		f.getCall = map[int32]int{}
	}
	f.getCall[player]++
	return f.values[player], nil
}

// ---- fixtures of OutputDeliveryServiceTest ----

const (
	odoRequestID   = "70297fd4-d412-4dbb-af05-6818fe0e687a"
	odoOperationID = "8065e700-9f48-36ba-ae8c-f881b28a28ef"
)

var (
	secretValues = bigs(90, 142)
	maskValues   = bigs(87, 111, 412, 313)
	tripleValues = [][]int64{{80, 62, 3719}, {72, 63, 32521}, {141, 264, 56212}, {19, 35, 612}}
)

func pairs(v ...[2]int64) []model.FactorPair {
	out := make([]model.FactorPair, len(v))
	for i, p := range v {
		out[i] = model.FactorPair{A: big.NewInt(p[0]), B: big.NewInt(p[1])}
	}
	return out
}

func odoFixture(t *testing.T) (*ODO, *fakeCastor, *fakePeers, *fakeInterim) {
	t.Helper()
	sp := testSpdz()
	zeroMac := make([]byte, spdz.WordWidth)
	fc := &fakeCastor{tuples: map[string][]castor.Tuple{}, err: map[string]error{}}
	var triples, masks []castor.Tuple
	for _, tr := range tripleValues {
		var tup castor.Tuple
		for _, v := range tr {
			tup = append(tup, castor.Share{Value: gfpBytes(t, sp, big.NewInt(v)), Mac: zeroMac})
		}
		triples = append(triples, tup)
	}
	for _, v := range maskValues {
		masks = append(masks, castor.Tuple{{Value: gfpBytes(t, sp, v), Mac: zeroMac}})
	}
	fc.tuples[castorKey(odoOperationID, castor.MultTripleGfp)] = triples
	fc.tuples[castorKey(odoRequestID, castor.InputMaskGfp)] = masks
	peers := &fakePeers{}
	interim := &fakeInterim{values: map[int32][]model.FactorPair{
		0: pairs([2]int64{10, 25}, [2]int64{39, 24}, [2]int64{1, 148}, [2]int64{294, 377}),
		1: pairs([2]int64{4, 63}, [2]int64{175, 136}, [2]int64{5, 106}, [2]int64{2, 27}),
	}}
	o := &ODO{Spdz: sp, Castor: fc, Peers: peers, Interim: interim, PlayerID: 0, NumPartners: 1,
		OpeningTimeout: 10 * time.Millisecond, PollInterval: time.Millisecond, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	return o, fc, peers, interim
}

func privatePairs() []model.FactorPair {
	return pairs([2]int64{90, 87}, [2]int64{111, 87}, [2]int64{142, 412}, [2]int64{313, 412})
}

func expectedExchange() model.ExchangeObject {
	return model.ExchangeObject{OperationID: odoOperationID, PlayerID: 0,
		Values: pairs([2]int64{10, 25}, [2]int64{39, 24}, [2]int64{1, 148}, [2]int64{294, 377})}
}

func wantModelErr(t *testing.T, err error, kind model.Kind, msg string) {
	t.Helper()
	var me *model.Error
	if !errors.As(err, &me) || me.Kind != kind || me.Msg != msg {
		t.Fatalf("got %v, want kind %d %q", err, kind, msg)
	}
}

func equalFactorPairs(a, b []model.FactorPair) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].A.Cmp(b[i].A) != 0 || a[i].B.Cmp(b[i].B) != 0 {
			return false
		}
	}
	return true
}

func TestMultiplySharesFetchingTriplesFails(t *testing.T) {
	o, fc, _, _ := odoFixture(t)
	fc.err[castorKey(odoOperationID, castor.MultTripleGfp)] = errors.New("No tuples")
	_, err := o.multiply(context.Background(), odoOperationID, privatePairs())
	wantModelErr(t, err, model.KindInternal, "Failed to retrieve the required Tuples form Castor")
}

func TestMultiplySharesDistributingValuesFails(t *testing.T) {
	o, _, peers, interim := odoFixture(t)
	peers.err = errors.New("Failed")
	_, err := o.multiply(context.Background(), odoOperationID, privatePairs())
	wantModelErr(t, err, model.KindInternal, fmt.Sprintf("Failed to open values for operation #%s", odoOperationID))
	if len(interim.put) != 1 || !reflect.DeepEqual(interim.put[0].OperationID, odoOperationID) || !equalFactorPairs(interim.put[0].Values, expectedExchange().Values) {
		t.Fatalf("%+v", interim.put)
	}
	if len(peers.opened) != 1 || !equalFactorPairs(peers.opened[0].Values, expectedExchange().Values) || peers.opened[0].PlayerID != 0 {
		t.Fatalf("%+v", peers.opened)
	}
}

func TestMultiplySharesReceivingAllOpenedSharesFails(t *testing.T) {
	o, _, peers, interim := odoFixture(t)
	interim.values[1] = nil // the partner never delivers
	_, err := o.multiply(context.Background(), odoOperationID, privatePairs())
	wantModelErr(t, err, model.KindInternal, fmt.Sprintf("Failed to open values for operation #%s", odoOperationID))
	if len(interim.put) != 1 || len(peers.opened) != 1 {
		t.Fatalf("put %d, open %d", len(interim.put), len(peers.opened))
	}
	if interim.getCall[0] != 1 {
		t.Fatalf("own values must be fetched exactly once, got %d", interim.getCall[0])
	}
	if interim.getCall[1] < 1 {
		t.Fatal("partner values must be polled at least once")
	}
}

func TestMultiplySharesSuccess(t *testing.T) {
	o, _, peers, interim := odoFixture(t)
	got, err := o.multiply(context.Background(), odoOperationID, privatePairs())
	if err != nil {
		t.Fatal(err)
	}
	want := bigs(12859, 91763, 95134, 138232)
	if len(got) != len(want) {
		t.Fatal(got)
	}
	for i := range want {
		if got[i].Cmp(want[i]) != 0 {
			t.Errorf("product %d: got %s want %s", i, got[i], want[i])
		}
	}
	if len(interim.put) != 1 || len(peers.opened) != 1 || interim.getCall[0] != 1 || interim.getCall[1] != 1 {
		t.Fatalf("put %d open %d get %v", len(interim.put), len(peers.opened), interim.getCall)
	}
	if !equalFactorPairs(interim.put[0].Values, expectedExchange().Values) {
		t.Fatalf("%+v", interim.put[0])
	}
}

func TestComputeODOCastorDoesNotReturnInputMasks(t *testing.T) {
	o, fc, _, _ := odoFixture(t)
	fc.err[castorKey(odoRequestID, castor.InputMaskGfp)] = errors.New("Failed fetching input masks")
	data := gfpBytes(t, testSpdz(), secretValues...)
	_, err := o.Compute(context.Background(), data, odoRequestID)
	wantModelErr(t, err, model.KindInternal, "Failed to retrieve the required Tuples form Castor")
	if len(fc.calls) != 1 || fc.calls[0] != (castorCall{odoRequestID, castor.InputMaskGfp, 4}) {
		t.Fatalf("%+v", fc.calls)
	}
}

func TestComputeODOSuccess(t *testing.T) {
	o, fc, peers, interim := odoFixture(t)
	sp := testSpdz()
	// the secret share data holds value||mac per word; the MAC (0) is of no interest here
	shareData := gfpBytes(t, sp, secretValues[0], big.NewInt(0), secretValues[1], big.NewInt(0))
	got, err := o.ComputeFromSecretShareData(context.Background(), shareData, odoRequestID)
	if err != nil {
		t.Fatal(err)
	}
	want := model.OutputDeliveryObject{
		SecretShares: gfpBytes(t, sp, secretValues...),
		RShares:      gfpBytes(t, sp, maskValues[0], maskValues[2]),
		VShares:      gfpBytes(t, sp, maskValues[1], maskValues[3]),
		WShares:      gfpBytes(t, sp, big.NewInt(12859), big.NewInt(95134)),
		UShares:      gfpBytes(t, sp, big.NewInt(91763), big.NewInt(138232)),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("\n got %+v\nwant %+v", got, want)
	}
	if len(interim.put) != 1 || len(peers.opened) != 1 {
		t.Fatalf("put %d open %d", len(interim.put), len(peers.opened))
	}
	if !equalFactorPairs(peers.opened[0].Values, expectedExchange().Values) || peers.opened[0].OperationID != odoOperationID {
		t.Fatalf("%+v", peers.opened[0])
	}
	// requests: 2n input masks for the request id, 2n triples for the derived operation id
	wantCalls := []castorCall{{odoRequestID, castor.InputMaskGfp, 4}, {odoOperationID, castor.MultTripleGfp, 4}}
	if !reflect.DeepEqual(fc.calls, wantCalls) {
		t.Fatalf("%+v", fc.calls)
	}
}

func TestOperationIDDerivation(t *testing.T) {
	if got := spdz.NameUUIDFromBytes([]byte(fmt.Sprintf("%s_%d", odoRequestID, 4))); got != odoOperationID {
		t.Fatal(got)
	}
}

// ---- SecretShareUtilTest (Service.convert) ----

func TestConvertToSecretShare(t *testing.T) {
	sp := testSpdz()
	mod := func(s string) *big.Int {
		v, _ := new(big.Int).SetString(s, 10)
		return v.Mod(v, sp.Prime)
	}
	svc := &Service{Spdz: sp, MacKey: mod("-33717010807885571165607137982809795379")}

	t.Run("givenLessInputMasksThanMaskedInput_whenConvertToSecretShare_thenFail", func(t *testing.T) {
		mi := &model.MaskedInput{SecretID: "93448822-fc76-4989-a927-450486ae0a08", HasData: true, Tags: []model.Tag{},
			Data: [][]byte{make([]byte, spdz.WordWidth)}}
		_, err := svc.convert(mi, nil)
		wantModelErr(t, err, model.KindBadRequest, "Received more input data than available inputMasks.")
	})

	t.Run("givenCorrectArguments_whenConvertToSecretShare_thenReturnExpectedResult", func(t *testing.T) {
		mi := &model.MaskedInput{SecretID: "3bcf8308-8f50-4d24-a37b-b0075bb5e779", HasData: true,
			Data: [][]byte{gfpBytes(t, sp, mod("37371993412255263319479925008425883363")), gfpBytes(t, sp, big.NewInt(0))}}
		masks := []javaser.Share{
			{Value: gfpBytes(t, sp, mod("-82730997414791468496799367418496881908")), Mac: gfpBytes(t, sp, mod("-60557275363670854182192939229091375859"))},
			{Value: gfpBytes(t, sp, mod("45359004002536205186084333850157344582")), Mac: gfpBytes(t, sp, mod("-48604663536222227589564560476962533035"))},
		}
		want := gfpBytes(t, sp,
			mod("-45359004002536205177319442410070998545"), mod("-170814686092998134911558977038957876158"),
			mod("45359004002536205186084333850157344582"), mod("-48604663536222227589564560476962533035"))
		got, err := svc.convert(mi, masks)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("\n got %x\nwant %x", got, want)
		}
	})
}

// MaskedInputDataTest: the 16 byte rule of MaskedInputData.of surfaces in the service as an
// IllegalArgumentException (400) from the gfp decoding.
func TestConvertToSecretShareRejectsWrongLength(t *testing.T) {
	sp := testSpdz()
	svc := &Service{Spdz: sp, MacKey: big.NewInt(1)}
	masks := []javaser.Share{{Value: make([]byte, spdz.WordWidth), Mac: make([]byte, spdz.WordWidth)}}
	for _, n := range []int{spdz.WordWidth - 1, spdz.WordWidth + 1} {
		mi := &model.MaskedInput{SecretID: "93448822-fc76-4989-a927-450486ae0a08", HasData: true, Data: [][]byte{make([]byte, n)}}
		_, err := svc.convert(mi, masks)
		wantModelErr(t, err, model.KindBadRequest, fmt.Sprintf("Gfp byte representation must have a length of 16. Actual: %d.", n))
	}
}
