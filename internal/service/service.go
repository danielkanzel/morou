// Package service holds the core routing and concurrency-control logic.
package service

import (
	"context"
	"errors"
	"time"

	"github.com/modelrouter/router/internal/balancer"
	"github.com/modelrouter/router/internal/config"
	"github.com/modelrouter/router/internal/health"
)

// Routing/limit errors surfaced to the HTTP layer.
var (
	// ErrUnknownModel means the model is not in the config (-> 404).
	ErrUnknownModel = errors.New("unknown model")
	// ErrNoHealthyBackend means no backend is currently available (-> 503).
	ErrNoHealthyBackend = errors.New("no healthy backend")
	// ErrSlotTimeout means a concurrency slot was not freed in time (-> 429).
	ErrSlotTimeout = errors.New("concurrency slot wait timed out")
)

// Service combines configuration, the backend monitor, the balancer and the
// per-(client,model) concurrency limiter.
type Service struct {
	cfg      *config.Config
	monitor  *health.Monitor
	balancer balancer.Balancer
	limiters *limiterRegistry
}

// New builds a Service.
func New(cfg *config.Config, mon *health.Monitor, bal balancer.Balancer) *Service {
	return &Service{
		cfg:      cfg,
		monitor:  mon,
		balancer: bal,
		limiters: newLimiterRegistry(cfg.Limit),
	}
}

// Config exposes the underlying configuration.
func (s *Service) Config() *config.Config { return s.cfg }

// Monitor exposes the backend monitor.
func (s *Service) Monitor() *health.Monitor { return s.monitor }

// AvailableModels returns model names that currently have a healthy backend.
func (s *Service) AvailableModels() []string {
	var out []string
	for _, m := range s.monitor.Models() {
		if s.monitor.HasHealthy(m) {
			out = append(out, m)
		}
	}
	return out
}

// Slot is a held concurrency slot that must be released when the request ends.
type Slot struct {
	pool     *slotPool
	released bool
}

// Release frees the slot. Safe to call multiple times.
func (s *Slot) Release() {
	if s == nil || s.released {
		return
	}
	s.released = true
	s.pool.release()
}

// AcquireResult reports the outcome of acquiring a concurrency slot.
type AcquireResult struct {
	Slot      *Slot
	WaitedFor time.Duration
}

// Acquire blocks until a concurrency slot for (cn, model) is free, bounded by
// the model's request timeout. It returns ErrSlotTimeout if the wait expires
// or the context is cancelled.
//
// The returned AcquireResult.WaitedFor reflects time spent waiting, for the
// queue-wait metric.
func (s *Service) Acquire(ctx context.Context, cn, model string) (AcquireResult, error) {
	pool := s.limiters.pool(cn, model)
	timeout := s.cfg.RequestTimeout(model)

	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()
	ok := pool.acquire(waitCtx)
	waited := time.Since(start)
	if !ok {
		// Distinguish parent cancellation from limit timeout where possible;
		// both map to "could not get a slot" but only the timeout is a 429.
		if ctx.Err() != nil {
			return AcquireResult{WaitedFor: waited}, ctx.Err()
		}
		return AcquireResult{WaitedFor: waited}, ErrSlotTimeout
	}
	return AcquireResult{Slot: &Slot{pool: pool}, WaitedFor: waited}, nil
}

// PickBackend selects a healthy backend for model via the balancer.
func (s *Service) PickBackend(model string) (*health.Backend, error) {
	if _, known := s.cfg.Models[model]; !known {
		return nil, ErrUnknownModel
	}
	healthy, ok := s.monitor.Healthy(model)
	if !ok {
		return nil, ErrUnknownModel
	}
	b := s.balancer.Pick(model, healthy)
	if b == nil {
		return nil, ErrNoHealthyBackend
	}
	return b, nil
}

// RequestTimeout returns the effective timeout for proxying a model request.
func (s *Service) RequestTimeout(model string) time.Duration {
	return s.cfg.RequestTimeout(model)
}
