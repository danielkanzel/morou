package health

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/modelrouter/router/internal/metrics"
)

// Monitor owns all backend pools and periodically refreshes engine detection,
// health status and queue depth.
type Monitor struct {
	mu     sync.RWMutex
	pools  map[string][]*Backend
	client *http.Client

	healthInterval time.Duration
	queueInterval  time.Duration
	useQueuePoll   bool

	log     *slog.Logger
	metrics *metrics.Metrics
}

// Options configures a Monitor.
type Options struct {
	HealthInterval time.Duration
	QueueInterval  time.Duration
	// PollQueue enables periodic queue-depth polling (lessQueue mode only).
	PollQueue bool
	Logger    *slog.Logger
	Metrics   *metrics.Metrics
}

// NewMonitor builds a Monitor from a map of model name to raw upstream URLs.
func NewMonitor(models map[string][]string, opts Options) *Monitor {
	pools := make(map[string][]*Backend, len(models))
	for model, urls := range models {
		bs := make([]*Backend, 0, len(urls))
		for _, u := range urls {
			bs = append(bs, NewBackend(model, u))
		}
		pools[model] = bs
	}
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Monitor{
		pools:          pools,
		client:         &http.Client{Timeout: 5 * time.Second},
		healthInterval: opts.HealthInterval,
		queueInterval:  opts.QueueInterval,
		useQueuePoll:   opts.PollQueue,
		log:            log,
		metrics:        opts.Metrics,
	}
}

// Healthy returns the list of currently-healthy backends for a model. The
// second return value is false when the model is unknown.
func (m *Monitor) Healthy(model string) ([]*Backend, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	pool, ok := m.pools[model]
	if !ok {
		return nil, false
	}
	out := make([]*Backend, 0, len(pool))
	for _, b := range pool {
		if b.Healthy() {
			out = append(out, b)
		}
	}
	return out, true
}

// HasHealthy reports whether a model currently has at least one healthy backend.
func (m *Monitor) HasHealthy(model string) bool {
	bs, ok := m.Healthy(model)
	return ok && len(bs) > 0
}

// Models returns the configured model names.
func (m *Monitor) Models() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]string, 0, len(m.pools))
	for name := range m.pools {
		out = append(out, name)
	}
	return out
}

// RangeBackends applies fn to every backend. Primarily used by tests to seed
// state without running the monitor loops.
func (m *Monitor) RangeBackends(fn func(*Backend)) {
	for _, b := range m.allBackends() {
		fn(b)
	}
}

// allBackends returns a flat snapshot of all backends.
func (m *Monitor) allBackends() []*Backend {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []*Backend
	for _, pool := range m.pools {
		out = append(out, pool...)
	}
	return out
}

// Start runs the monitor loops until ctx is cancelled. It performs an initial
// synchronous detection + health pass before returning so the first requests
// see accurate state.
func (m *Monitor) Start(ctx context.Context) {
	m.detectEngines(ctx)
	m.checkAll(ctx)

	go m.healthLoop(ctx)
	if m.useQueuePoll {
		m.pollQueues(ctx)
		go m.queueLoop(ctx)
	}
}

func (m *Monitor) healthLoop(ctx context.Context) {
	t := time.NewTicker(m.healthInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.checkAll(ctx)
		}
	}
}

func (m *Monitor) queueLoop(ctx context.Context) {
	t := time.NewTicker(m.queueInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.pollQueues(ctx)
		}
	}
}

func (m *Monitor) detectEngines(ctx context.Context) {
	var wg sync.WaitGroup
	for _, b := range m.allBackends() {
		if b.Engine() != EngineUnknown {
			continue
		}
		wg.Add(1)
		go func(b *Backend) {
			defer wg.Done()
			e := m.detectEngine(ctx, b)
			if e != EngineUnknown {
				b.setEngine(e)
				m.log.Info("detected backend engine", "model", b.Model, "url", b.Raw, "engine", string(e))
			} else {
				m.log.Warn("could not detect backend engine", "model", b.Model, "url", b.Raw)
			}
		}(b)
	}
	wg.Wait()
}

// detectEngine probes a backend to determine whether it is vLLM or SGLang.
// Strategy: sglang exposes /get_server_info; vllm exposes a vllm:* prefixed
// Prometheus metric on /metrics. We try sglang first, then vllm.
func (m *Monitor) detectEngine(ctx context.Context, b *Backend) Engine {
	if m.probeOK(ctx, b, "/get_server_info") {
		return EngineSGLang
	}
	body, ok := m.probeBody(ctx, b, "/metrics")
	if ok && strings.Contains(body, "vllm:") {
		return EngineVLLM
	}
	// Fall back to the health endpoints to disambiguate.
	if m.probeOK(ctx, b, "/health_generate") {
		return EngineSGLang
	}
	if m.probeOK(ctx, b, "/health") {
		return EngineVLLM
	}
	return EngineUnknown
}

func (m *Monitor) checkAll(ctx context.Context) {
	var wg sync.WaitGroup
	for _, b := range m.allBackends() {
		wg.Add(1)
		go func(b *Backend) {
			defer wg.Done()
			m.checkOne(ctx, b)
		}(b)
	}
	wg.Wait()
}

func (m *Monitor) checkOne(ctx context.Context, b *Backend) {
	// Engine may still be unknown if detection failed earlier; retry it.
	if b.Engine() == EngineUnknown {
		if e := m.detectEngine(ctx, b); e != EngineUnknown {
			b.setEngine(e)
		}
	}

	var path string
	switch b.Engine() {
	case EngineSGLang:
		path = "/health_generate"
	case EngineVLLM:
		path = "/health"
	default:
		// Unknown engine: try the generic vllm health endpoint.
		path = "/health"
	}

	ok := m.probeOK(ctx, b, path)
	prev := b.Healthy()
	b.setHealthy(ok)
	if m.metrics != nil {
		v := 0.0
		if ok {
			v = 1.0
		}
		m.metrics.BackendUp.WithLabelValues(b.Model, b.Raw).Set(v)
	}
	if prev != ok {
		if ok {
			m.log.Info("backend healthy", "model", b.Model, "url", b.Raw)
		} else {
			m.log.Warn("backend unhealthy", "model", b.Model, "url", b.Raw)
		}
	}
}

func (m *Monitor) pollQueues(ctx context.Context) {
	var wg sync.WaitGroup
	for _, b := range m.allBackends() {
		if !b.Healthy() {
			continue
		}
		wg.Add(1)
		go func(b *Backend) {
			defer wg.Done()
			n, ok := m.queueDepth(ctx, b)
			if !ok {
				return
			}
			b.setQueueSize(n)
			if m.metrics != nil {
				m.metrics.BackendQueueSize.WithLabelValues(b.Model, b.Raw).Set(float64(n))
			}
		}(b)
	}
	wg.Wait()
}

// queueDepth fetches the current waiting-queue depth for a backend.
func (m *Monitor) queueDepth(ctx context.Context, b *Backend) (int64, bool) {
	switch b.Engine() {
	case EngineVLLM:
		body, ok := m.probeBody(ctx, b, "/metrics")
		if !ok {
			return 0, false
		}
		return parseVLLMWaiting(body)
	case EngineSGLang:
		body, ok := m.probeBody(ctx, b, "/get_server_info")
		if !ok {
			return 0, false
		}
		return parseSGLangQueue(body)
	default:
		return 0, false
	}
}

// --- HTTP helpers ---

func (m *Monitor) probeOK(ctx context.Context, b *Backend, path string) bool {
	resp, err := m.get(ctx, b, path)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	drain(resp.Body)
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

func (m *Monitor) probeBody(ctx context.Context, b *Backend, path string) (string, bool) {
	resp, err := m.get(ctx, b, path)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		drain(resp.Body)
		return "", false
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", false
	}
	return string(data), true
}

func (m *Monitor) get(ctx context.Context, b *Backend, path string) (*http.Response, error) {
	target := strings.TrimRight(b.URL.String(), "/") + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	return m.client.Do(req)
}

func drain(r io.Reader) {
	_, _ = io.Copy(io.Discard, io.LimitReader(r, 1<<20))
}

// parseVLLMWaiting extracts the vllm:num_requests_waiting gauge value from a
// Prometheus exposition body. Values across model labels are summed.
func parseVLLMWaiting(body string) (int64, bool) {
	const metric = "vllm:num_requests_waiting"
	var total float64
	found := false
	sc := bufio.NewScanner(strings.NewReader(body))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.HasPrefix(line, metric) {
			continue
		}
		// Skip if the metric name continues with other characters (prefix match).
		rest := line[len(metric):]
		if rest != "" && rest[0] != ' ' && rest[0] != '{' && rest[0] != '\t' {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		v, err := strconv.ParseFloat(fields[len(fields)-1], 64)
		if err != nil {
			continue
		}
		total += v
		found = true
	}
	if !found {
		return 0, false
	}
	return int64(total), true
}

// parseSGLangQueue extracts a waiting-queue depth from /get_server_info JSON.
// SGLang exposes internal state under various keys depending on version; we
// look for a few well-known ones.
func parseSGLangQueue(body string) (int64, bool) {
	var raw map[string]any
	if err := json.Unmarshal([]byte(body), &raw); err != nil {
		return 0, false
	}
	// Common candidates seen across SGLang versions.
	candidates := []string{
		"num_requests_waiting",
		"waiting_queue_size",
		"queue_size",
		"num_queue_reqs",
	}
	// Some versions nest the info under "internal_states" (a list of dicts).
	scopes := []map[string]any{raw}
	if states, ok := raw["internal_states"].([]any); ok {
		for _, s := range states {
			if sm, ok := s.(map[string]any); ok {
				scopes = append(scopes, sm)
			}
		}
	}
	for _, scope := range scopes {
		for _, key := range candidates {
			if v, ok := scope[key]; ok {
				if n, ok := toInt64(v); ok {
					return n, true
				}
			}
		}
	}
	return 0, false
}

func toInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case float64:
		return int64(n), true
	case int:
		return int64(n), true
	case int64:
		return n, true
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			f, ferr := n.Float64()
			if ferr != nil {
				return 0, false
			}
			return int64(f), true
		}
		return i, true
	default:
		return 0, false
	}
}
