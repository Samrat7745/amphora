// Package rediscache stores input masks and interim values in Redis under the Java key names,
// with Java-serialized values (readable by and compatible with the Java service).
package rediscache

import (
	"context"
	"errors"
	"fmt"

	"github.com/Samrat7745/amphora/go/internal/javaser"
	"github.com/Samrat7745/amphora/go/internal/model"
	"github.com/redis/go-redis/v9"
)

// Cache provides the two stores.
type Cache struct {
	rdb           redis.UniversalClient
	inputPrefix   string
	interimPrefix string
}

// New creates a cache; prefixes are the store names (without the "::" suffix).
func New(rdb redis.UniversalClient, inputMaskStore, interimValueStore string) *Cache {
	return &Cache{rdb: rdb, inputPrefix: inputMaskStore + "::", interimPrefix: interimValueStore + "::"}
}

// Ping checks Redis reachability.
func (c *Cache) Ping(ctx context.Context) error { return c.rdb.Ping(ctx).Err() }

// PutInputMasks stores the input mask shares (no TTL).
func (c *Cache) PutInputMasks(ctx context.Context, requestID string, shares []javaser.Share) error {
	return c.rdb.Set(ctx, c.inputPrefix+requestID, javaser.EncodeInputMaskListFromCastor(shares), 0).Err()
}

// GetInputMasks returns (nil, false) when absent.
func (c *Cache) GetInputMasks(ctx context.Context, requestID string) ([]javaser.Share, bool, error) {
	b, err := c.rdb.Get(ctx, c.inputPrefix+requestID).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	shares, err := javaser.DecodeInputMaskList(b)
	if err != nil {
		return nil, false, fmt.Errorf("unreadable cached input masks for request %s: %w", requestID, err)
	}
	return shares, true, nil
}

// DeleteInputMasks removes the entry.
func (c *Cache) DeleteInputMasks(ctx context.Context, requestID string) error {
	return c.rdb.Del(ctx, c.inputPrefix+requestID).Err()
}

func (c *Cache) interimKey(opID string, player int32) string {
	if opID == "" {
		opID = "null" // Java string concatenation of a null UUID
	}
	return fmt.Sprintf("%s%s_%d", c.interimPrefix, opID, player)
}

// PutInterim stores an exchange object (overwrites, no TTL).
func (c *Cache) PutInterim(ctx context.Context, e model.ExchangeObject) error {
	je := javaser.ExchangeObject{OperationID: e.OperationID, PlayerID: e.PlayerID}
	if e.Values != nil {
		je.Values = []javaser.FactorPair{} // a null list (absent in the request) stays null, [] stays empty
	}
	for _, p := range e.Values {
		je.Values = append(je.Values, javaser.FactorPair{A: p.A, B: p.B})
	}
	return c.rdb.Set(ctx, c.interimKey(e.OperationID, e.PlayerID), javaser.EncodeExchangeObject(je), 0).Err()
}

// GetAndEvictInterim atomically reads and deletes the entry; (nil, nil) when absent or unreadable
// (Java: Try(...).toOption(), the key is deleted either way).
func (c *Cache) GetAndEvictInterim(ctx context.Context, opID string, player int32) ([]model.FactorPair, error) {
	key := c.interimKey(opID, player)
	pipe := c.rdb.TxPipeline()
	get := pipe.Get(ctx, key)
	pipe.Del(ctx, key)
	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
		return nil, err
	}
	b, err := get.Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, nil
		}
		return nil, err
	}
	e, err := javaser.DecodeExchangeObject(b)
	if err != nil {
		return nil, nil
	}
	out := make([]model.FactorPair, len(e.Values))
	for i, p := range e.Values {
		out[i] = model.FactorPair{A: p.A, B: p.B}
	}
	return out, nil
}
