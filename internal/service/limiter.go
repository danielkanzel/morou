package service

import (
	"context"
	"sync"
)

// slotPool is a counting semaphore that supports context-aware acquisition.
// A nil/zero-capacity pool means unlimited.
type slotPool struct {
	ch chan struct{}
}

func newSlotPool(max int) *slotPool {
	if max <= 0 {
		return &slotPool{} // unlimited
	}
	return &slotPool{ch: make(chan struct{}, max)}
}

// acquire tries to take a slot, blocking until one is free or ctx is done.
// It returns true if a slot was acquired. For unlimited pools it returns
// immediately.
func (p *slotPool) acquire(ctx context.Context) bool {
	if p.ch == nil {
		return true
	}
	select {
	case p.ch <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

// release returns a previously acquired slot. It is a no-op for unlimited pools.
func (p *slotPool) release() {
	if p.ch == nil {
		return
	}
	select {
	case <-p.ch:
	default:
	}
}

// limiterRegistry lazily creates one slotPool per (client, model) pair.
type limiterRegistry struct {
	mu    sync.Mutex
	pools map[string]*slotPool
	// limitFn returns the configured max and whether a limit exists.
	limitFn func(cn, model string) (int, bool)
}

func newLimiterRegistry(limitFn func(cn, model string) (int, bool)) *limiterRegistry {
	return &limiterRegistry{
		pools:   make(map[string]*slotPool),
		limitFn: limitFn,
	}
}

func (r *limiterRegistry) pool(cn, model string) *slotPool {
	key := cn + "\x00" + model
	r.mu.Lock()
	defer r.mu.Unlock()
	if p, ok := r.pools[key]; ok {
		return p
	}
	max, has := r.limitFn(cn, model)
	if !has {
		max = 0 // unlimited
	}
	p := newSlotPool(max)
	r.pools[key] = p
	return p
}
