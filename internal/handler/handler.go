// Package handler implements the HTTP layer: OpenAI endpoints, /metrics, /docs
// and liveness probes.
package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/modelrouter/router/internal/auth"
	"github.com/modelrouter/router/internal/metrics"
	"github.com/modelrouter/router/internal/proxy"
	"github.com/modelrouter/router/internal/service"
)

// maxBodyForModelPeek caps how much of the request body we buffer to read the
// "model" field. Bodies larger than this are still proxied in full.
const maxBodyForModelPeek = 8 << 20 // 8 MiB

// Handler wires the service, proxy, auth and metrics into an http.Handler.
type Handler struct {
	svc     *service.Service
	prx     *proxy.Proxy
	auth    *auth.Authenticator
	metrics *metrics.Metrics
	log     *slog.Logger
}

// New builds the Handler.
func New(svc *service.Service, prx *proxy.Proxy, a *auth.Authenticator, m *metrics.Metrics, log *slog.Logger) *Handler {
	if log == nil {
		log = slog.Default()
	}
	return &Handler{svc: svc, prx: prx, auth: a, metrics: m, log: log}
}

// Routes returns the configured http.ServeMux.
func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /v1/chat/completions", h.handleProxy)
	mux.HandleFunc("POST /v1/completions", h.handleProxy)
	mux.HandleFunc("POST /v1/embeddings", h.handleProxy)
	mux.HandleFunc("GET /v1/models", h.handleModels)

	mux.Handle("GET /metrics", promhttp.HandlerFor(h.metrics.Registry(), promhttp.HandlerOpts{}))
	mux.HandleFunc("GET /docs", h.handleDocs)
	mux.HandleFunc("GET /openapi.json", h.handleOpenAPI)
	mux.HandleFunc("GET /health", h.handleLiveness)
	mux.HandleFunc("GET /healthz", h.handleLiveness)

	return mux
}

func (h *Handler) handleLiveness(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

func (h *Handler) handleModels(w http.ResponseWriter, r *http.Request) {
	// Identify the client for parity with proxy endpoints (401/403 semantics).
	if _, err := h.auth.Identify(r); err != nil {
		h.writeAuthError(w, err)
		return
	}
	type model struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		OwnedBy string `json:"owned_by"`
	}
	now := time.Now().Unix()
	data := make([]model, 0)
	for _, name := range h.svc.AvailableModels() {
		data = append(data, model{ID: name, Object: "model", Created: now, OwnedBy: "model-router"})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
}

func (h *Handler) handleProxy(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	// 1. Identify the client.
	id, err := h.auth.Identify(r)
	if err != nil {
		h.writeAuthError(w, err)
		return
	}

	// 2. Read body and extract the model name.
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyForModelPeek))
	if err != nil {
		h.writeError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	_ = r.Body.Close()
	modelName, err := extractModel(body)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, "request body missing or invalid 'model' field")
		return
	}
	// Restore the body for the downstream proxy.
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))

	labels := func(code int) {
		h.metrics.RequestsTotal.WithLabelValues(id.Name, modelName, strconv.Itoa(code)).Inc()
		h.metrics.RequestDuration.WithLabelValues(id.Name, modelName).Observe(time.Since(start).Seconds())
	}

	// 3. Validate the model exists and has a healthy backend early so we fail
	// fast before queueing.
	if _, known := h.svc.Config().Models[modelName]; !known {
		h.writeError(w, http.StatusNotFound, "model not found: "+modelName)
		labels(http.StatusNotFound)
		return
	}
	if !h.svc.Monitor().HasHealthy(modelName) {
		h.writeError(w, http.StatusServiceUnavailable, "no healthy backend for model: "+modelName)
		labels(http.StatusServiceUnavailable)
		return
	}

	// 4. Acquire a concurrency slot (queue/wait/429).
	res, err := h.svc.Acquire(r.Context(), id.CN, modelName)
	h.metrics.QueueWaitSeconds.WithLabelValues(id.Name, modelName).Observe(res.WaitedFor.Seconds())
	if err != nil {
		if errors.Is(err, service.ErrSlotTimeout) {
			h.metrics.ConcurrencyRejected.WithLabelValues(id.Name, modelName).Inc()
			h.writeError(w, http.StatusTooManyRequests, "concurrency limit reached, slot wait timed out")
			labels(http.StatusTooManyRequests)
			return
		}
		// Parent context cancelled: the client went away while queued.
		labels(499)
		h.log.Info("client cancelled while queued", "client", id.Name, "model", modelName)
		return
	}
	defer res.Slot.Release()

	// 5. Pick a backend.
	backend, err := h.svc.PickBackend(modelName)
	if err != nil {
		code := http.StatusServiceUnavailable
		if errors.Is(err, service.ErrUnknownModel) {
			code = http.StatusNotFound
		}
		h.writeError(w, code, err.Error())
		labels(code)
		return
	}

	// 6. Track inflight and proxy.
	h.metrics.InflightRequests.WithLabelValues(id.Name, modelName).Inc()
	defer h.metrics.InflightRequests.WithLabelValues(id.Name, modelName).Dec()

	cw := &codeWriter{ResponseWriter: w, code: http.StatusOK}
	h.log.Info("proxying request",
		"client", id.Name, "model", modelName, "path", r.URL.Path, "backend", backend.Raw)

	h.prx.Forward(cw, r, backend.URL, h.svc.RequestTimeout(modelName))

	labels(cw.code)
	h.log.Info("request completed",
		"client", id.Name, "model", modelName, "code", cw.code,
		"duration_ms", time.Since(start).Milliseconds(), "backend", backend.Raw)
}

func (h *Handler) writeAuthError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, auth.ErrNoCertificate):
		h.writeError(w, http.StatusUnauthorized, "client certificate required")
	case errors.Is(err, auth.ErrUnknownClient):
		h.writeError(w, http.StatusForbidden, "client not authorized")
	default:
		h.writeError(w, http.StatusUnauthorized, "authentication failed")
	}
}

func (h *Handler) writeError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"message": msg,
			"type":    httpErrorType(code),
			"code":    code,
		},
	})
}

func httpErrorType(code int) string {
	switch code {
	case http.StatusUnauthorized:
		return "authentication_error"
	case http.StatusForbidden:
		return "permission_error"
	case http.StatusNotFound:
		return "not_found_error"
	case http.StatusTooManyRequests:
		return "rate_limit_error"
	case http.StatusServiceUnavailable:
		return "service_unavailable_error"
	default:
		return "invalid_request_error"
	}
}

// extractModel reads the "model" field from an OpenAI JSON request body.
func extractModel(body []byte) (string, error) {
	var probe struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return "", err
	}
	if probe.Model == "" {
		return "", errors.New("missing model field")
	}
	return probe.Model, nil
}

// codeWriter records the status code while transparently forwarding writes,
// preserving streaming by exposing Flush.
type codeWriter struct {
	http.ResponseWriter
	code        int
	wroteHeader bool
}

func (c *codeWriter) WriteHeader(code int) {
	if !c.wroteHeader {
		c.code = code
		c.wroteHeader = true
	}
	c.ResponseWriter.WriteHeader(code)
}

func (c *codeWriter) Write(b []byte) (int, error) {
	if !c.wroteHeader {
		c.wroteHeader = true
	}
	return c.ResponseWriter.Write(b)
}

func (c *codeWriter) Flush() {
	if f, ok := c.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
