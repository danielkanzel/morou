// Package balancer selects an upstream backend among the healthy candidates.
package balancer

import (
	"math/rand"
	"sync"

	"github.com/modelrouter/router/internal/config"
	"github.com/modelrouter/router/internal/health"
)

// Balancer picks one backend from a slice of healthy candidates.
type Balancer interface {
	// Pick returns a backend for the given model, or nil if none are available.
	Pick(model string, healthy []*health.Backend) *health.Backend
}

// New returns the Balancer implementation for the configured strategy.
func New(strategy config.LoadBalancing) Balancer {
	switch strategy {
	case config.LBRoundRobin:
		return &roundRobin{cursors: make(map[string]*uint64)}
	case config.LBLessQueue:
		return lessQueue{}
	case config.LBRandom:
		fallthrough
	default:
		return random{}
	}
}

type random struct{}

func (random) Pick(_ string, healthy []*health.Backend) *health.Backend {
	if len(healthy) == 0 {
		return nil
	}
	return healthy[rand.Intn(len(healthy))] //nolint:gosec // balancing does not need crypto randomness
}

type roundRobin struct {
	mu      sync.Mutex
	cursors map[string]*uint64
}

func (r *roundRobin) Pick(model string, healthy []*health.Backend) *health.Backend {
	if len(healthy) == 0 {
		return nil
	}
	r.mu.Lock()
	c, ok := r.cursors[model]
	if !ok {
		var v uint64
		c = &v
		r.cursors[model] = c
	}
	idx := *c % uint64(len(healthy))
	*c++
	r.mu.Unlock()
	return healthy[idx]
}

type lessQueue struct{}

func (lessQueue) Pick(_ string, healthy []*health.Backend) *health.Backend {
	if len(healthy) == 0 {
		return nil
	}
	best := healthy[0]
	bestQ := best.QueueSize()
	for _, b := range healthy[1:] {
		if q := b.QueueSize(); q < bestQ {
			best, bestQ = b, q
		}
	}
	return best
}
