package service

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSlotPoolUnlimited(t *testing.T) {
	p := newSlotPool(0)
	for i := 0; i < 1000; i++ {
		if !p.acquire(context.Background()) {
			t.Fatalf("unlimited pool refused acquire")
		}
	}
}

func TestSlotPoolBlocksAtCapacity(t *testing.T) {
	p := newSlotPool(2)
	if !p.acquire(context.Background()) {
		t.Fatal("acquire 1 failed")
	}
	if !p.acquire(context.Background()) {
		t.Fatal("acquire 2 failed")
	}
	// Third acquire must block until timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if p.acquire(ctx) {
		t.Fatal("acquire 3 should have timed out")
	}
	// Release one, now acquire should succeed.
	p.release()
	if !p.acquire(context.Background()) {
		t.Fatal("acquire after release failed")
	}
}

func TestSlotPoolReleaseUnblocks(t *testing.T) {
	p := newSlotPool(1)
	if !p.acquire(context.Background()) {
		t.Fatal("initial acquire failed")
	}

	acquired := make(chan struct{})
	go func() {
		p.acquire(context.Background())
		close(acquired)
	}()

	select {
	case <-acquired:
		t.Fatal("second acquire should be blocked")
	case <-time.After(30 * time.Millisecond):
	}

	p.release()
	select {
	case <-acquired:
	case <-time.After(time.Second):
		t.Fatal("release did not unblock waiter")
	}
}

// TestConcurrencyNeverExceedsMax hammers a pool with many goroutines and
// verifies the in-flight count never exceeds the configured maximum.
func TestConcurrencyNeverExceedsMax(t *testing.T) {
	const max = 4
	p := newSlotPool(max)

	var inflight, peak int64
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if !p.acquire(context.Background()) {
				return
			}
			cur := atomic.AddInt64(&inflight, 1)
			for {
				old := atomic.LoadInt64(&peak)
				if cur <= old || atomic.CompareAndSwapInt64(&peak, old, cur) {
					break
				}
			}
			time.Sleep(time.Millisecond)
			atomic.AddInt64(&inflight, -1)
			p.release()
		}()
	}
	wg.Wait()
	if peak > max {
		t.Fatalf("peak concurrency %d exceeded max %d", peak, max)
	}
}

func TestLimiterRegistryReusesPool(t *testing.T) {
	r := newLimiterRegistry(func(_, _ string) (int, bool) { return 3, true })
	a := r.pool("cn", "model")
	b := r.pool("cn", "model")
	if a != b {
		t.Fatal("registry should return the same pool for the same key")
	}
	c := r.pool("cn", "other")
	if a == c {
		t.Fatal("different model should get a different pool")
	}
}

func TestLimiterRegistryUnlimitedWhenNoLimit(t *testing.T) {
	r := newLimiterRegistry(func(_, _ string) (int, bool) { return 0, false })
	p := r.pool("cn", "model")
	for i := 0; i < 100; i++ {
		if !p.acquire(context.Background()) {
			t.Fatal("expected unlimited pool")
		}
	}
}
