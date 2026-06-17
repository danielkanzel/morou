// Package health owns backend pools and monitors their health and queue depth.
package health

import (
	"net/url"
	"sync/atomic"
)

// Engine identifies the inference server software behind an upstream.
type Engine string

const (
	// EngineVLLM is the vLLM OpenAI server.
	EngineVLLM Engine = "vllm"
	// EngineSGLang is the SGLang server.
	EngineSGLang Engine = "sglang"
	// EngineUnknown means the engine has not been detected yet.
	EngineUnknown Engine = "unknown"
)

// Backend represents a single upstream instance within a model group.
type Backend struct {
	Model string
	URL   *url.URL
	Raw   string

	engine    atomic.Value // Engine
	healthy   atomic.Bool
	queueSize atomic.Int64
}

// NewBackend builds a Backend from a model name and raw URL. The URL is assumed
// to be valid (config validation enforces this).
func NewBackend(model, raw string) *Backend {
	u, _ := url.Parse(raw)
	b := &Backend{Model: model, URL: u, Raw: raw}
	b.engine.Store(EngineUnknown)
	return b
}

// Engine returns the detected engine.
func (b *Backend) Engine() Engine {
	e, _ := b.engine.Load().(Engine)
	return e
}

func (b *Backend) setEngine(e Engine) { b.engine.Store(e) }

// Healthy reports whether the backend is currently healthy.
func (b *Backend) Healthy() bool { return b.healthy.Load() }

func (b *Backend) setHealthy(v bool) { b.healthy.Store(v) }

// QueueSize returns the last observed queue depth.
func (b *Backend) QueueSize() int64 { return b.queueSize.Load() }

func (b *Backend) setQueueSize(v int64) { b.queueSize.Store(v) }

// SetQueueForTest sets the queue size directly. Intended for tests only.
func (b *Backend) SetQueueForTest(v int64) { b.setQueueSize(v) }

// SetHealthyForTest sets health status directly. Intended for tests only.
func (b *Backend) SetHealthyForTest(v bool) { b.setHealthy(v) }

// SetEngineForTest sets the engine directly. Intended for tests only.
func (b *Backend) SetEngineForTest(e Engine) { b.setEngine(e) }
