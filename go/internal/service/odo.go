// Package service holds the business logic (Java: StorageService, OutputDeliveryService,
// InputMaskCachingService, SecretShareUtil).
package service

import (
	"context"
	"fmt"
	"log/slog"
	"math/big"
	"sync"
	"time"

	"github.com/carbynestack/amphora-go/internal/castor"
	"github.com/carbynestack/amphora-go/internal/model"
	"github.com/carbynestack/amphora-go/spdz"
)

// TupleSource downloads tuple shares from Castor.
type TupleSource interface {
	Download(ctx context.Context, requestID, tupleType string, count int64) ([]castor.Tuple, error)
}

// Peers distributes exchange objects to the other players.
type Peers interface {
	Open(ctx context.Context, e model.ExchangeObject) error
}

// InterimStore keeps the exchanged interim values.
type InterimStore interface {
	PutInterim(ctx context.Context, e model.ExchangeObject) error
	GetAndEvictInterim(ctx context.Context, opID string, player int32) ([]model.FactorPair, error)
}

// ODO computes output delivery objects (Beaver multiplication with the peers).
type ODO struct {
	Spdz           *spdz.Utils
	Castor         TupleSource
	Peers          Peers // nil: single player
	Interim        InterimStore
	PlayerID       int
	NumPartners    int
	OpeningTimeout time.Duration
	PollInterval   time.Duration // default 5ms
	Log            *slog.Logger
}

const castorFailure = "Failed to retrieve the required Tuples form Castor"

func (o *ODO) log() *slog.Logger {
	if o.Log != nil {
		return o.Log
	}
	return slog.Default()
}

// ComputeFromSecretShareData uses the value halves (first 16 bytes) of each 32 byte word.
func (o *ODO) ComputeFromSecretShareData(ctx context.Context, data []byte, requestID string) (model.OutputDeliveryObject, error) {
	n := len(data) / spdz.ShareWidth
	vals := make([]byte, n*spdz.WordWidth)
	for i := 0; i < n; i++ {
		copy(vals[i*spdz.WordWidth:], data[i*spdz.ShareWidth:i*spdz.ShareWidth+spdz.WordWidth])
	}
	return o.Compute(ctx, vals, requestID)
}

// Compute is computeOutputDeliveryObject(byte[] shareData, UUID requestId).
func (o *ODO) Compute(ctx context.Context, shareData []byte, requestID string) (model.OutputDeliveryObject, error) {
	var odo model.OutputDeliveryObject
	n := len(shareData) / spdz.WordWidth
	masks, err := o.Castor.Download(ctx, requestID, castor.InputMaskGfp, int64(n)*2)
	if err != nil {
		o.log().Debug("failed to retrieve input masks", "err", err)
		return odo, model.Internal(castorFailure)
	}
	if len(masks) < 2*n {
		return odo, model.Internal("Index %d out of bounds for length %d", 2*n-1, len(masks))
	}
	size := n * spdz.WordWidth
	odo.SecretShares = make([]byte, size)
	odo.RShares = make([]byte, size)
	odo.VShares = make([]byte, size)
	odo.WShares = make([]byte, size)
	odo.UShares = make([]byte, size)
	pairs := make([]model.FactorPair, 0, 2*n)
	for i := 0; i < n; i++ {
		secret := shareData[i*spdz.WordWidth : (i+1)*spdz.WordWidth]
		if len(masks[2*i]) < 1 || len(masks[2*i+1]) < 1 {
			return odo, model.Internal("Index 0 out of bounds for length 0")
		}
		m1, m2 := masks[2*i][0].Value, masks[2*i+1][0].Value
		s, err := o.Spdz.FromGfp(secret)
		if err != nil {
			return odo, model.BadRequest("%s", err.Error())
		}
		r, err := o.Spdz.FromGfp(m1)
		if err != nil {
			return odo, model.BadRequest("%s", err.Error())
		}
		v, err := o.Spdz.FromGfp(m2)
		if err != nil {
			return odo, model.BadRequest("%s", err.Error())
		}
		copy(odo.SecretShares[i*spdz.WordWidth:], secret)
		copy(odo.RShares[i*spdz.WordWidth:], m1)
		copy(odo.VShares[i*spdz.WordWidth:], m2)
		pairs = append(pairs, model.FactorPair{A: s, B: r}, model.FactorPair{A: v, B: r})
	}
	opID := spdz.NameUUIDFromBytes([]byte(fmt.Sprintf("%s_%d", requestID, len(pairs))))
	o.log().Debug("using operation id for multiplications", "operationId", opID, "requestId", requestID)
	products, err := o.multiply(ctx, opID, pairs)
	if err != nil {
		return odo, err
	}
	for i := 0; i < n; i++ {
		w, err := o.Spdz.ToGfp(products[2*i])
		if err != nil {
			return odo, model.BadRequest("%s", err.Error())
		}
		u, err := o.Spdz.ToGfp(products[2*i+1])
		if err != nil {
			return odo, model.BadRequest("%s", err.Error())
		}
		copy(odo.WShares[i*spdz.WordWidth:], w)
		copy(odo.UShares[i*spdz.WordWidth:], u)
	}
	return odo, nil
}

func openFailure(opID string) error {
	return model.Internal("Failed to open values for operation #%s", opID)
}

func (o *ODO) multiply(ctx context.Context, opID string, pairs []model.FactorPair) ([]*big.Int, error) {
	triples, err := o.Castor.Download(ctx, opID, castor.MultTripleGfp, int64(len(pairs)))
	if err != nil {
		o.log().Debug("failed to retrieve triples", "err", err)
		return nil, model.Internal(castorFailure)
	}
	if len(triples) < len(pairs) {
		return nil, model.Internal("Index %d out of bounds for length %d", len(pairs)-1, len(triples))
	}
	type abc struct{ a, b, c *big.Int }
	tv := make([]abc, len(pairs))
	diffs := make([]model.FactorPair, len(pairs))
	for i, p := range pairs {
		if len(triples[i]) < 3 {
			return nil, model.Internal("Index %d out of bounds for length %d", len(triples[i]), len(triples[i]))
		}
		var e [3]*big.Int
		for k := 0; k < 3; k++ {
			e[k], err = o.Spdz.FromGfp(triples[i][k].Value)
			if err != nil {
				return nil, model.BadRequest("%s", err.Error())
			}
		}
		tv[i] = abc{e[0], e[1], e[2]}
		diffs[i] = model.FactorPair{A: new(big.Int).Sub(p.A, e[0]), B: new(big.Int).Sub(p.B, e[1])}
	}
	exch := model.ExchangeObject{OperationID: opID, PlayerID: int32(o.PlayerID), Values: diffs}
	if err := o.Interim.PutInterim(ctx, exch); err != nil {
		return nil, model.Internal("%s", err.Error())
	}
	if o.Peers != nil && o.NumPartners > 0 {
		if err := o.Peers.Open(ctx, exch); err != nil {
			o.log().Error("Failed to distribute own shares for operation", "operationId", opID, "err", err)
			return nil, openFailure(opID)
		}
	}
	opened, err := o.recombine(ctx, opID, len(pairs))
	if err != nil {
		o.log().Error("Failed to recombine shares of operation", "operationId", opID, "err", err)
		return nil, openFailure(opID)
	}
	out := make([]*big.Int, len(pairs))
	p := o.Spdz.Prime
	for i := range pairs {
		d1, d2 := opened[i].A, opened[i].B
		sh := new(big.Int).Set(tv[i].c)
		sh.Add(sh, new(big.Int).Mul(d1, tv[i].b))
		sh.Add(sh, new(big.Int).Mul(d2, tv[i].a))
		sh.Mod(sh, p)
		if o.PlayerID == 0 {
			sh.Add(sh, new(big.Int).Mul(d1, d2))
			sh.Mod(sh, p)
		}
		out[i] = sh
	}
	return out, nil
}

// recombine polls the interim values of all players 0..numPartners (inclusive) and sums them.
func (o *ODO) recombine(ctx context.Context, opID string, want int) ([]model.FactorPair, error) {
	players := o.NumPartners + 1
	results := make([][]model.FactorPair, players)
	errs := make([]error, players)
	deadline := time.Now().Add(o.OpeningTimeout)
	poll := o.PollInterval
	if poll <= 0 {
		poll = 5 * time.Millisecond
	}
	var wg sync.WaitGroup
	for j := 0; j < players; j++ {
		wg.Add(1)
		go func(j int) {
			defer wg.Done()
			for {
				vals, err := o.Interim.GetAndEvictInterim(ctx, opID, int32(j))
				if err == nil && vals != nil {
					results[j] = vals
					return
				}
				if time.Now().After(deadline) || ctx.Err() != nil {
					if err == nil {
						err = fmt.Errorf("timeout waiting for interim values of player %d", j)
					}
					errs[j] = err
					return
				}
				time.Sleep(poll)
			}
		}(j)
	}
	wg.Wait()
	for _, e := range errs {
		if e != nil {
			return nil, e
		}
	}
	acc := results[0]
	p := o.Spdz.Prime
	for j := 1; j < players; j++ {
		next := make([]model.FactorPair, len(acc))
		for i := range acc {
			if i >= len(results[j]) {
				return nil, fmt.Errorf("player %d sent too few interim values", j)
			}
			next[i] = model.FactorPair{
				A: new(big.Int).Mod(new(big.Int).Add(acc[i].A, results[j][i].A), p),
				B: new(big.Int).Mod(new(big.Int).Add(acc[i].B, results[j][i].B), p),
			}
		}
		acc = next
	}
	if players == 1 {
		// Java: Future.reduce of a single element returns it unreduced
		acc = results[0]
	}
	if len(acc) < want {
		return nil, fmt.Errorf("received %d interim values, expected %d", len(acc), want)
	}
	return acc, nil
}
