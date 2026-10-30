package rediscache

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

// Two players' interim values for the same operation id arrive in both orders and concurrently; every
// value is readable exactly once (eviction) even with competing readers.

func TestInterimBothOrdersAndConcurrent(t *testing.T) {
	c, _, ctx := setup(t)
	for rep, order := range [][]int32{{0, 1}, {1, 0}} {
		op := fmt.Sprintf("op-order-%d", rep)
		for _, p := range order {
			if err := c.PutInterim(ctx, exchange(op, p, 3)); err != nil {
				t.Fatal(err)
			}
		}
		for _, p := range []int32{0, 1} {
			v, err := c.GetAndEvictInterim(ctx, op, p)
			if err != nil || len(v) != 3 {
				t.Fatalf("player %d: %v %v", p, v, err)
			}
			if again, _ := c.GetAndEvictInterim(ctx, op, p); again != nil {
				t.Fatalf("player %d value must be evicted", p)
			}
		}
	}
	// concurrent writers and competing readers
	for rep := 0; rep < 20; rep++ {
		op := fmt.Sprintf("op-conc-%d", rep)
		var wg sync.WaitGroup
		var got [2]int32
		for p := int32(0); p < 2; p++ {
			wg.Add(1)
			go func(p int32) {
				defer wg.Done()
				if err := c.PutInterim(ctx, exchange(op, p, 2)); err != nil {
					t.Error(err)
				}
			}(p)
		}
		wg.Wait()
		for r := 0; r < 4; r++ {
			for p := int32(0); p < 2; p++ {
				wg.Add(1)
				go func(p int32) {
					defer wg.Done()
					v, err := c.GetAndEvictInterim(ctx, op, p)
					if err != nil {
						t.Error(err)
					}
					if v != nil {
						atomic.AddInt32(&got[p], 1)
					}
				}(p)
			}
		}
		wg.Wait()
		if got[0] != 1 || got[1] != 1 {
			t.Fatalf("rep %d: each interim value must be consumed exactly once, got %v", rep, got)
		}
	}
}
