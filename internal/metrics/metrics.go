// Package metrics defines the Prometheus metrics exported by edge-limiter.
package metrics

import (
	"context"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/HappyVirgo/edge-limiter/limiter"
)

// Metrics bundles the collectors on a private registry.
type Metrics struct {
	Registry       *prometheus.Registry
	HTTPRequests   *prometheus.CounterVec
	Checks         *prometheus.CounterVec
	BackendLatency *prometheus.HistogramVec
	Fallbacks      *prometheus.CounterVec
}

// New registers all collectors, including Go runtime and process metrics.
func New() *Metrics {
	m := &Metrics{
		Registry: prometheus.NewRegistry(),
		HTTPRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "edge_limiter_http_requests_total",
			Help: "HTTP requests served, by handler and status code.",
		}, []string{"handler", "code"}),
		Checks: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "edge_limiter_checks_total",
			Help: "Rate-limit decisions, by rule and result (allowed|denied|error).",
		}, []string{"rule", "result"}),
		BackendLatency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "edge_limiter_backend_latency_seconds",
			Help:    "Latency of backend Allow calls, by backend and outcome (ok|error).",
			Buckets: []float64{.00005, .0001, .00025, .0005, .001, .0025, .005, .01, .025, .05, .1},
		}, []string{"backend", "outcome"}),
		Fallbacks: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "edge_limiter_fallback_total",
			Help: "Decisions made without the primary backend, by failure mode.",
		}, []string{"mode"}),
	}
	m.Registry.MustRegister(m.HTTPRequests, m.Checks, m.BackendLatency, m.Fallbacks,
		collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	return m
}

// Handler serves the registry in the Prometheus exposition format.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{Registry: m.Registry})
}

// RecordDecision counts one decision for rule.
func (m *Metrics) RecordDecision(rule string, allowed bool, err error) {
	result := "denied"
	switch {
	case err != nil:
		result = "error"
	case allowed:
		result = "allowed"
	}
	m.Checks.WithLabelValues(rule, result).Inc()
}

// Instrument wraps b so every Allow call is timed.
func (m *Metrics) Instrument(b limiter.Backend) limiter.Backend {
	return &instrumented{Backend: b, ok: m.BackendLatency.WithLabelValues(b.Name(), "ok"),
		failed: m.BackendLatency.WithLabelValues(b.Name(), "error")}
}

type instrumented struct {
	limiter.Backend
	ok, failed prometheus.Observer
}

func (i *instrumented) Allow(ctx context.Context, key string, cost int64, l limiter.Limit) (limiter.Decision, error) {
	start := time.Now()
	d, err := i.Backend.Allow(ctx, key, cost, l)
	if err != nil {
		i.failed.Observe(time.Since(start).Seconds())
	} else {
		i.ok.Observe(time.Since(start).Seconds())
	}
	return d, err
}

// Ping forwards to the wrapped backend when it supports health checks.
func (i *instrumented) Ping(ctx context.Context) error {
	if p, ok := i.Backend.(limiter.Pinger); ok {
		return p.Ping(ctx)
	}
	return nil
}
