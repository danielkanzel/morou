// Package metrics defines and registers the router's Prometheus metrics.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Metrics bundles all Prometheus collectors used by the router.
type Metrics struct {
	RequestsTotal       *prometheus.CounterVec
	RequestDuration     *prometheus.HistogramVec
	InflightRequests    *prometheus.GaugeVec
	QueueWaitSeconds    *prometheus.HistogramVec
	ConcurrencyRejected *prometheus.CounterVec
	BackendUp           *prometheus.GaugeVec
	BackendQueueSize    *prometheus.GaugeVec

	registry *prometheus.Registry
}

// New creates a Metrics bundle registered against a fresh registry.
func New() *Metrics {
	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector())
	reg.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	factory := promauto.With(reg)

	return &Metrics{
		registry: reg,
		RequestsTotal: factory.NewCounterVec(prometheus.CounterOpts{
			Name: "router_requests_total",
			Help: "Total number of proxied requests.",
		}, []string{"client", "model", "code"}),
		RequestDuration: factory.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "router_request_duration_seconds",
			Help:    "Latency of proxied requests in seconds.",
			Buckets: prometheus.DefBuckets,
		}, []string{"client", "model"}),
		InflightRequests: factory.NewGaugeVec(prometheus.GaugeOpts{
			Name: "router_inflight_requests",
			Help: "Current number of in-flight requests per client and model.",
		}, []string{"client", "model"}),
		QueueWaitSeconds: factory.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "router_queue_wait_seconds",
			Help:    "Time spent waiting for a concurrency slot in seconds.",
			Buckets: prometheus.DefBuckets,
		}, []string{"client", "model"}),
		ConcurrencyRejected: factory.NewCounterVec(prometheus.CounterOpts{
			Name: "router_concurrency_rejected_total",
			Help: "Total number of requests rejected due to the concurrency limit (429).",
		}, []string{"client", "model"}),
		BackendUp: factory.NewGaugeVec(prometheus.GaugeOpts{
			Name: "router_backend_up",
			Help: "Backend health status (1 = healthy, 0 = unhealthy).",
		}, []string{"model", "url"}),
		BackendQueueSize: factory.NewGaugeVec(prometheus.GaugeOpts{
			Name: "router_backend_queue_size",
			Help: "Observed backend queue depth (used by lessQueue balancing).",
		}, []string{"model", "url"}),
	}
}

// Registry exposes the underlying Prometheus registry for the /metrics handler.
func (m *Metrics) Registry() *prometheus.Registry {
	return m.registry
}
