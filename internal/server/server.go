// Package server exposes the rate limiter over HTTP: the check API, health
// and readiness probes, and Prometheus metrics.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/HappyVirgo/edge-limiter/internal/config"
	"github.com/HappyVirgo/edge-limiter/internal/metrics"
	"github.com/HappyVirgo/edge-limiter/limiter"
)

const maxKeyLen = 256

// Server holds the dependencies of the HTTP API.
type Server struct {
	cfg     *config.Config
	backend limiter.Backend
	metrics *metrics.Metrics
	log     *slog.Logger
	ready   atomic.Bool
}

// New returns a Server that is ready to receive traffic.
func New(cfg *config.Config, backend limiter.Backend, m *metrics.Metrics, log *slog.Logger) *Server {
	s := &Server{cfg: cfg, backend: backend, metrics: m, log: log}
	s.ready.Store(true)
	return s
}

// SetReady toggles readiness; it is cleared at the start of graceful shutdown
// so load balancers stop routing new requests before connections drain.
func (s *Server) SetReady(v bool) { s.ready.Store(v) }

// Handler returns the HTTP routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	route := func(pattern, name string, h http.HandlerFunc) {
		counter := s.metrics.HTTPRequests.MustCurryWith(map[string]string{"handler": name})
		mux.Handle(pattern, promhttp.InstrumentHandlerCounter(counter, h))
	}
	route("POST /v1/check", "check", s.handleCheck)
	route("GET /healthz", "healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	route("GET /readyz", "readyz", s.handleReady)
	mux.Handle("GET /metrics", s.metrics.Handler())
	return mux
}

// CheckRequest is the body of POST /v1/check. Route and Tenant are optional
// and select a configured rule; Cost defaults to 1.
type CheckRequest struct {
	Key    string `json:"key"`
	Cost   int64  `json:"cost"`
	Route  string `json:"route,omitempty"`
	Tenant string `json:"tenant,omitempty"`
}

// CheckResponse is the body returned by POST /v1/check.
type CheckResponse struct {
	Allowed    bool      `json:"allowed"`
	Rule       string    `json:"rule"`
	Limit      int64     `json:"limit"`
	Remaining  int64     `json:"remaining"`
	ResetAt    time.Time `json:"reset_at"`
	RetryAfter float64   `json:"retry_after"` // seconds; 0 when allowed
}

func (s *Server) handleCheck(w http.ResponseWriter, r *http.Request) {
	var req CheckRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if req.Key == "" || len(req.Key) > maxKeyLen {
		writeError(w, http.StatusBadRequest, "key must be 1-256 bytes")
		return
	}
	if req.Cost == 0 {
		req.Cost = 1
	}
	rule := s.cfg.Resolve(req.Route, req.Tenant)
	d, err := s.backend.Allow(r.Context(), rule.Name+":"+req.Key, req.Cost, rule.Limit)
	s.metrics.RecordDecision(rule.Name, d.Allowed, err)
	switch {
	case errors.Is(err, limiter.ErrInvalidCost):
		writeError(w, http.StatusBadRequest, err.Error())
		return
	case err != nil:
		s.log.ErrorContext(r.Context(), "check failed", "rule", rule.Name, "err", err)
		writeError(w, http.StatusServiceUnavailable, "rate limiter backend unavailable")
		return
	}
	writeJSON(w, http.StatusOK, CheckResponse{
		Allowed:    d.Allowed,
		Rule:       rule.Name,
		Limit:      d.Limit,
		Remaining:  d.Remaining,
		ResetAt:    d.ResetAt.UTC(),
		RetryAfter: d.RetryAfter.Seconds(),
	})
}

// handleReady fails while shutting down, and when the backend is unreachable
// and the service is configured to fail closed (it could only deny).
func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	if !s.ready.Load() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "shutting_down"})
		return
	}
	p, ok := s.backend.(limiter.Pinger)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Second)
	defer cancel()
	if err := p.Ping(ctx); err != nil {
		if s.cfg.Backend.FailureMode == limiter.FailClosed {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "backend_unavailable"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "degraded", "failure_mode": string(s.cfg.Backend.FailureMode)})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
