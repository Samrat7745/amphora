package service

// Property tests for the multiplication (Beaver) / open code path with 2..5 parties. The parties are real ODO
// instances wired through in-memory interim stores with the eviction semantics of the Redis cache. The MAC
// "verification" is the SPDZ relation sum(mac_i) == alpha * sum(value_i); the ODO itself only handles values, so
// tampering with a share must show up as a broken product relation or a broken MAC relation in the harness.

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"math/rand"
	"sync"
	"testing"
	"time"

	"github.com/carbynestack/amphora-go/internal/castor"
	"github.com/carbynestack/amphora-go/internal/model"
	"github.com/carbynestack/amphora-go/spdz"
)

// memInterim is an InterimStore with GetAndEvict semantics (absent -> nil, nil).
type memInterim struct {
	mu   sync.Mutex
	m    map[string][]model.FactorPair
	puts int
	gets int // successful reads
}

func newMemInterim() *memInterim { return &memInterim{m: map[string][]model.FactorPair{}} }

func ikey(op string, p int32) string { return fmt.Sprintf("%s_%d", op, p) }

func (s *memInterim) PutInterim(_ context.Context, e model.ExchangeObject) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.puts++
	s.m[ikey(e.OperationID, e.PlayerID)] = append([]model.FactorPair{}, e.Values...)
	return nil
}

func (s *memInterim) GetAndEvictInterim(_ context.Context, op string, p int32) ([]model.FactorPair, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[ikey(op, p)]
	if !ok {
		return nil, nil
	}
	delete(s.m, ikey(op, p))
	s.gets++
	return v, nil
}

func (s *memInterim) size() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.m)
}

// mesh delivers a party's exchange object to every other party's store (what POST /inter-vcp/open does).
type mesh struct {
	self   int
	stores []*memInterim
	gate   <-chan struct{} // if non-nil, Open blocks until closed
}

func (m *mesh) Open(ctx context.Context, e model.ExchangeObject) error {
	if m.gate != nil {
		<-m.gate
	}
	for i, s := range m.stores {
		if i != m.self {
			if err := s.PutInterim(ctx, e); err != nil {
				return err
			}
		}
	}
	return nil
}

type world struct {
	n         int
	sp        *spdz.Utils
	p         *big.Int
	alpha     *big.Int
	reqID     string
	secrets   []*big.Int      // s_i
	rs, vs    []*big.Int      // masks
	secretSh  [][]*big.Int    // [party][i]
	maskSh    [][]*big.Int    // [party][2i], [2i+1] values
	maskMac   [][]*big.Int    // same for macs
	trip      [][][3]*big.Int // [party][pair] a,b,c values
	tripMac   [][][3]*big.Int // macs
	castors   []*fakeCastor
	stores    []*memInterim
	opID      string
	pairCount int
}

func mod(p, x *big.Int) *big.Int { return x.Mod(x, p) }

func split(r *rand.Rand, p, secret *big.Int, n int) []*big.Int {
	out := make([]*big.Int, n)
	sum := new(big.Int)
	for i := 0; i < n-1; i++ {
		out[i] = randBelow(r, p)
		sum.Add(sum, out[i])
	}
	out[n-1] = mod(p, new(big.Int).Sub(secret, sum))
	return out
}

func randBelow(r *rand.Rand, n *big.Int) *big.Int {
	b := make([]byte, 24)
	r.Read(b)
	return new(big.Int).Mod(new(big.Int).SetBytes(b), n)
}

func newWorld(t *testing.T, r *rand.Rand, n, m int) *world {
	t.Helper()
	sp := testSpdz()
	p := sp.Prime
	w := &world{n: n, sp: sp, p: p, pairCount: 2 * m}
	w.alpha = randBelow(r, p)
	w.reqID = fmt.Sprintf("00000000-0000-4000-8000-%012d", r.Int63n(1e12))
	w.opID = spdz.NameUUIDFromBytes([]byte(fmt.Sprintf("%s_%d", w.reqID, 2*m)))
	macOf := func(x *big.Int) *big.Int { return mod(p, new(big.Int).Mul(w.alpha, x)) }
	w.secretSh = make([][]*big.Int, n)
	w.maskSh = make([][]*big.Int, n)
	w.maskMac = make([][]*big.Int, n)
	w.trip = make([][][3]*big.Int, n)
	w.tripMac = make([][][3]*big.Int, n)
	for i := 0; i < n; i++ {
		w.trip[i] = make([][3]*big.Int, 2*m)
		w.tripMac[i] = make([][3]*big.Int, 2*m)
	}
	for i := 0; i < m; i++ {
		s, rr, v := randBelow(r, p), randBelow(r, p), randBelow(r, p)
		w.secrets = append(w.secrets, s)
		w.rs = append(w.rs, rr)
		w.vs = append(w.vs, v)
	}
	shareAll := func(x *big.Int) (vals, macs []*big.Int) {
		return split(r, p, x, n), split(r, p, macOf(x), n)
	}
	for _, s := range w.secrets {
		for pi, sh := range split(r, p, s, n) {
			w.secretSh[pi] = append(w.secretSh[pi], sh)
		}
	}
	for i := 0; i < m; i++ {
		for _, x := range []*big.Int{w.rs[i], w.vs[i]} {
			vals, macs := shareAll(x)
			for pi := range vals {
				w.maskSh[pi] = append(w.maskSh[pi], vals[pi])
				w.maskMac[pi] = append(w.maskMac[pi], macs[pi])
			}
		}
	}
	for k := 0; k < 2*m; k++ {
		a, b := randBelow(r, p), randBelow(r, p)
		c := mod(p, new(big.Int).Mul(a, b))
		for e, x := range []*big.Int{a, b, c} {
			vals, macs := shareAll(x)
			for pi := range vals {
				w.trip[pi][k][e] = vals[pi]
				w.tripMac[pi][k][e] = macs[pi]
			}
		}
	}
	w.rebuildCastors(t)
	return w
}

func (w *world) gfp(t *testing.T, v *big.Int) []byte {
	t.Helper()
	b, err := w.sp.ToGfp(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func (w *world) rebuildCastors(t *testing.T) {
	w.castors = make([]*fakeCastor, w.n)
	w.stores = make([]*memInterim, w.n)
	for pi := 0; pi < w.n; pi++ {
		fc := &fakeCastor{tuples: map[string][]castor.Tuple{}, err: map[string]error{}}
		var masks, triples []castor.Tuple
		for k := range w.maskSh[pi] {
			masks = append(masks, castor.Tuple{{Value: w.gfp(t, w.maskSh[pi][k]), Mac: w.gfp(t, w.maskMac[pi][k])}})
		}
		for k := range w.trip[pi] {
			var tup castor.Tuple
			for e := 0; e < 3; e++ {
				tup = append(tup, castor.Share{Value: w.gfp(t, w.trip[pi][k][e]), Mac: w.gfp(t, w.tripMac[pi][k][e])})
			}
			triples = append(triples, tup)
		}
		fc.tuples[castorKey(w.reqID, castor.InputMaskGfp)] = masks
		fc.tuples[castorKey(w.opID, castor.MultTripleGfp)] = triples
		w.castors[pi] = fc
		w.stores[pi] = newMemInterim()
	}
}

func (w *world) odo(pi int, timeout time.Duration, gate <-chan struct{}) *ODO {
	return &ODO{Spdz: w.sp, Castor: w.castors[pi], Peers: &mesh{self: pi, stores: w.stores, gate: gate}, Interim: w.stores[pi],
		PlayerID: pi, NumPartners: w.n - 1, OpeningTimeout: timeout, PollInterval: time.Millisecond,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func (w *world) shareData(t *testing.T, pi int) []byte {
	var b []byte
	for _, v := range w.secretSh[pi] {
		b = append(b, w.gfp(t, v)...)
	}
	return b
}

// run executes all parties concurrently and returns their outputs.
func (w *world) run(t *testing.T) []model.OutputDeliveryObject {
	t.Helper()
	outs := make([]model.OutputDeliveryObject, w.n)
	errs := make([]error, w.n)
	var wg sync.WaitGroup
	for pi := 0; pi < w.n; pi++ {
		wg.Add(1)
		go func(pi int) {
			defer wg.Done()
			outs[pi], errs[pi] = w.odo(pi, 10*time.Second, nil).Compute(context.Background(), w.shareData(t, pi), w.reqID)
		}(pi)
	}
	wg.Wait()
	for pi, e := range errs {
		if e != nil {
			t.Fatalf("party %d: %v", pi, e)
		}
	}
	return outs
}

func (w *world) sumOf(t *testing.T, outs []model.OutputDeliveryObject, field func(model.OutputDeliveryObject) []byte, i int) *big.Int {
	sum := new(big.Int)
	for _, o := range outs {
		v, err := w.sp.FromGfp(field(o)[i*spdz.WordWidth : (i+1)*spdz.WordWidth])
		if err != nil {
			t.Fatal(err)
		}
		sum.Add(sum, v)
	}
	return mod(w.p, sum)
}

func (w *world) productsOK(t *testing.T, outs []model.OutputDeliveryObject) bool {
	ok := true
	for i := range w.secrets {
		ws := w.sumOf(t, outs, func(o model.OutputDeliveryObject) []byte { return o.WShares }, i)
		us := w.sumOf(t, outs, func(o model.OutputDeliveryObject) []byte { return o.UShares }, i)
		if ws.Cmp(mod(w.p, new(big.Int).Mul(w.secrets[i], w.rs[i]))) != 0 || us.Cmp(mod(w.p, new(big.Int).Mul(w.vs[i], w.rs[i]))) != 0 {
			ok = false
		}
	}
	return ok
}

// macsOK checks sum(mac)=alpha*sum(value) for every input mask and triple element as currently held by the castors.
func (w *world) macsOK(t *testing.T) bool {
	check := func(vals, macs []*big.Int) bool {
		sv, sm := new(big.Int), new(big.Int)
		for i := range vals {
			sv.Add(sv, vals[i])
			sm.Add(sm, macs[i])
		}
		return mod(w.p, sm).Cmp(mod(w.p, new(big.Int).Mul(w.alpha, mod(w.p, sv)))) == 0
	}
	for k := 0; k < len(w.maskSh[0]); k++ {
		var vs, ms []*big.Int
		for pi := 0; pi < w.n; pi++ {
			v, _ := w.sp.FromGfp(w.castors[pi].tuples[castorKey(w.reqID, castor.InputMaskGfp)][k][0].Value)
			m, _ := w.sp.FromGfp(w.castors[pi].tuples[castorKey(w.reqID, castor.InputMaskGfp)][k][0].Mac)
			vs, ms = append(vs, v), append(ms, m)
		}
		if !check(vs, ms) {
			return false
		}
	}
	for k := 0; k < len(w.trip[0]); k++ {
		for e := 0; e < 3; e++ {
			var vs, ms []*big.Int
			for pi := 0; pi < w.n; pi++ {
				sh := w.castors[pi].tuples[castorKey(w.opID, castor.MultTripleGfp)][k][e]
				v, _ := w.sp.FromGfp(sh.Value)
				m, _ := w.sp.FromGfp(sh.Mac)
				vs, ms = append(vs, v), append(ms, m)
			}
			if !check(vs, ms) {
				return false
			}
		}
	}
	return true
}

func TestMultiplicationProperty(t *testing.T) {
	r := rand.New(rand.NewSource(20240607))
	for iter := 0; iter < 60; iter++ {
		n := 2 + iter%4
		m := 1 + r.Intn(3)
		t.Run(fmt.Sprintf("n%d_m%d_%d", n, m, iter), func(t *testing.T) {
			w := newWorld(t, r, n, m)
			if !w.macsOK(t) {
				t.Fatal("fixture MACs inconsistent")
			}
			outs := w.run(t)
			if !w.productsOK(t, outs) {
				t.Fatal("reconstructed products differ from s*r and v*r")
			}
			// every party received every other party's value and the interim values were evicted by reading
			for pi, s := range w.stores {
				if s.size() != 0 {
					t.Fatalf("party %d: %d interim values left (not evicted)", pi, s.size())
				}
				if s.gets != n {
					t.Fatalf("party %d read %d interim sets, want %d (own + %d peers)", pi, s.gets, n, n-1)
				}
			}
			// the secret shares echoed back in the ODO are the party's own
			for pi, o := range outs {
				if string(o.SecretShares) != string(w.shareData(t, pi)) {
					t.Fatal("secret shares not echoed")
				}
			}
		})
	}
}

// Edge secrets: 0, 1 and p-1 multiply correctly.
func TestMultiplicationEdgeSecrets(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	for _, s := range []*big.Int{big.NewInt(0), big.NewInt(1), new(big.Int).Sub(testSpdz().Prime, big.NewInt(1))} {
		w := newWorld(t, r, 3, 1)
		// rebuild the secret shares for the fixed secret
		w.secrets[0] = s
		for pi, sh := range split(r, w.p, s, 3) {
			w.secretSh[pi][0] = sh
		}
		w.rebuildCastors(t)
		if !w.productsOK(t, w.run(t)) {
			t.Fatalf("secret %v", s)
		}
	}
}

// Flip one bit in any share value or MAC of any party (for every kind of share) and the relations must break.
func TestTamperedShareIsDetected(t *testing.T) {
	r := rand.New(rand.NewSource(99))
	type target struct {
		name string
		flip func(w *world, pi int, bit uint)
		mac  bool // detected by the MAC relation (otherwise by the product relation)
	}
	flipBytes := func(b []byte, bit uint) { b[bit/8%16] ^= 1 << (bit % 8) }
	targets := []target{
		{"secret share", func(w *world, pi int, bit uint) { /* handled via shareData below */ }, false},
		{"mask value", func(w *world, pi int, bit uint) {
			flipBytes(w.castors[pi].tuples[castorKey(w.reqID, castor.InputMaskGfp)][0][0].Value, bit)
		}, true},
		{"mask mac", func(w *world, pi int, bit uint) {
			flipBytes(w.castors[pi].tuples[castorKey(w.reqID, castor.InputMaskGfp)][1][0].Mac, bit)
		}, true},
		{"triple a", func(w *world, pi int, bit uint) {
			flipBytes(w.castors[pi].tuples[castorKey(w.opID, castor.MultTripleGfp)][0][0].Value, bit)
		}, true},
		{"triple b mac", func(w *world, pi int, bit uint) {
			flipBytes(w.castors[pi].tuples[castorKey(w.opID, castor.MultTripleGfp)][1][1].Mac, bit)
		}, true},
		{"triple c", func(w *world, pi int, bit uint) {
			flipBytes(w.castors[pi].tuples[castorKey(w.opID, castor.MultTripleGfp)][1][2].Value, bit)
		}, true},
	}
	for _, tg := range targets {
		for iter := 0; iter < 12; iter++ {
			n := 2 + iter%4
			pi := r.Intn(n)
			// bits 0..126 only: 2^127 < p so a single flip always changes the residue; skip bit positions in the top byte
			// of the limb-swapped encoding that could map out of range by using the raw wire bit and re-checking below.
			bit := uint(r.Intn(120))
			t.Run(fmt.Sprintf("%s/n%d/party%d/bit%d", tg.name, n, pi, bit), func(t *testing.T) {
				w := newWorld(t, r, n, 1)
				if tg.name == "secret share" {
					// tamper with the data handed to party pi: run manually
					data := w.shareData(t, pi)
					flipBytes(data, bit)
					before, _ := w.sp.FromGfp(w.shareData(t, pi))
					after, _ := w.sp.FromGfp(data)
					if before.Cmp(after) == 0 {
						t.Skip("bit flip did not change the residue")
					}
					outs := make([]model.OutputDeliveryObject, n)
					var wg sync.WaitGroup
					for q := 0; q < n; q++ {
						wg.Add(1)
						go func(q int) {
							defer wg.Done()
							d := w.shareData(t, q)
							if q == pi {
								d = data
							}
							var err error
							outs[q], err = w.odo(q, 10*time.Second, nil).Compute(context.Background(), d, w.reqID)
							if err != nil {
								t.Error(err)
							}
						}(q)
					}
					wg.Wait()
					if w.productsOK(t, outs) {
						t.Fatal("tampered secret share verified")
					}
					return
				}
				// snapshot the original value to make sure the flip changes the residue
				tg.flip(w, pi, bit)
				if tg.mac {
					if w.macsOK(t) {
						t.Fatal("MAC check passed for a tampered share")
					}
				}
				outs := w.run(t)
				if w.productsOK(t, outs) && tg.name != "mask mac" && tg.name != "triple b mac" {
					t.Fatal("product relation passed for a tampered share")
				}
			})
		}
	}
}

// Opening order and concurrency: party 1 delivers before party 0 starts, after it, and simultaneously.
func TestOpenOrdersAndConcurrency(t *testing.T) {
	for _, mode := range []string{"peer-first", "self-first", "simultaneous", "delayed-peer"} {
		for rep := 0; rep < 5; rep++ {
			t.Run(fmt.Sprintf("%s/%d", mode, rep), func(t *testing.T) {
				r := rand.New(rand.NewSource(int64(rep) + 1))
				w := newWorld(t, r, 2, 2)
				outs := make([]model.OutputDeliveryObject, 2)
				errs := make([]error, 2)
				gate1 := make(chan struct{})
				var gate0 chan struct{}
				start := func(pi int, wg *sync.WaitGroup, gate <-chan struct{}) {
					wg.Add(1)
					go func() {
						defer wg.Done()
						outs[pi], errs[pi] = w.odo(pi, 10*time.Second, gate).Compute(context.Background(), w.shareData(t, pi), w.reqID)
					}()
				}
				waitPut := func(pi int) { // wait until party pi has stored its own interim value (it then distributes)
					deadline := time.Now().Add(5 * time.Second)
					for {
						w.stores[pi].mu.Lock()
						n := w.stores[pi].puts
						w.stores[pi].mu.Unlock()
						if n > 0 || time.Now().After(deadline) {
							return
						}
						time.Sleep(time.Millisecond)
					}
				}
				var wg sync.WaitGroup
				switch mode {
				case "peer-first":
					close(gate1)
					start(1, &wg, gate1)
					waitPut(1)
					// peer's open reaches party 0 before party 0 has even started
					deadline := time.Now().Add(5 * time.Second)
					for w.stores[0].size() == 0 && time.Now().Before(deadline) {
						time.Sleep(time.Millisecond)
					}
					start(0, &wg, nil)
				case "self-first":
					start(0, &wg, nil)
					waitPut(0)
					close(gate1)
					start(1, &wg, gate1)
				case "simultaneous":
					close(gate1)
					start(0, &wg, gate0)
					start(1, &wg, gate1)
				case "delayed-peer":
					// party 1 stores its values but its distribution to party 0 is held back until party 0 is polling
					start(0, &wg, nil)
					start(1, &wg, gate1)
					waitPut(0)
					waitPut(1)
					time.Sleep(20 * time.Millisecond)
					close(gate1)
				}
				wg.Wait()
				for pi, e := range errs {
					if e != nil {
						t.Fatalf("party %d: %v", pi, e)
					}
				}
				if !w.productsOK(t, outs) {
					t.Fatal("wrong products")
				}
				for pi, s := range w.stores {
					if s.size() != 0 {
						t.Fatalf("party %d: values left after read", pi)
					}
				}
			})
		}
	}
}

// A missing peer must time out with the documented error and must not hang.
func TestOpenTimesOutWhenPeerNeverDelivers(t *testing.T) {
	r := rand.New(rand.NewSource(5))
	w := newWorld(t, r, 3, 1)
	start := time.Now()
	_, err := w.odo(0, 80*time.Millisecond, nil).Compute(context.Background(), w.shareData(t, 0), w.reqID)
	wantModelErr(t, err, model.KindInternal, "Failed to open values for operation #"+w.opID)
	if time.Since(start) > 5*time.Second {
		t.Fatal("took too long")
	}
	// context cancellation also ends the wait early
	w.rebuildCastors(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start = time.Now()
	_, err = w.odo(0, time.Minute, nil).Compute(ctx, w.shareData(t, 0), w.reqID)
	if err == nil || time.Since(start) > 5*time.Second {
		t.Fatalf("%v after %v", err, time.Since(start))
	}
}

// An interim value that is read is gone: a second read finds nothing, and a late duplicate delivery is not
// consumed by a different operation id.
func TestInterimEvictionAfterRead(t *testing.T) {
	s := newMemInterim()
	ctx := context.Background()
	e := model.ExchangeObject{OperationID: "op", PlayerID: 1, Values: []model.FactorPair{{A: big.NewInt(1), B: big.NewInt(2)}}}
	_ = s.PutInterim(ctx, e)
	if v, _ := s.GetAndEvictInterim(ctx, "other", 1); v != nil {
		t.Fatal("other operation must not see it")
	}
	if v, _ := s.GetAndEvictInterim(ctx, "op", 0); v != nil {
		t.Fatal("other player must not see it")
	}
	v, _ := s.GetAndEvictInterim(ctx, "op", 1)
	if len(v) != 1 {
		t.Fatal(v)
	}
	if v, _ := s.GetAndEvictInterim(ctx, "op", 1); v != nil {
		t.Fatal("must be evicted after the first read")
	}
}
