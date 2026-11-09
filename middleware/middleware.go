// Package middleware provides an embeddable net/http rate-limiting middleware
// backed by any limiter.Backend (in-memory, Redis, or a Failover of both).
package middleware

import (
	"encoding/json"
	"math"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/HappyVirgo/edge-limiter/limiter"
)

// Policy chooses the rule name and limit that apply to a request.
type Policy func(r *http.Request) (rule string, limit limiter.Limit)

// Options configures the middleware. Backend and Policy are required.
type Options struct {
	Backend limiter.Backend
	Policy  Policy
	// Key identifies the caller; defaults to ClientIP.
	Key func(r *http.Request) string
	// Cost returns how many units the request consumes; defaults to 1.
	Cost func(r *http.Request) int64
	// OnError handles backend errors. The default lets the request through
	// (fail open); wrap the backend in limiter.Failover for richer behaviour.
	OnError func(w http.ResponseWriter, r *http.Request, next http.Handler, err error)
	// OnDecision, if set, observes every decision (e.g. for metrics).
	OnDecision func(r *http.Request, rule string, d limiter.Decision, err error)
}

// StaticPolicy applies one limit to every request.
func StaticPolicy(rule string, l limiter.Limit) Policy {
	return func(*http.Request) (string, limiter.Limit) { return rule, l }
}

// ClientIP returns the host part of r.RemoteAddr. It deliberately ignores
// X-Forwarded-For, which clients can spoof; use HeaderKey behind a trusted proxy.
func ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// HeaderKey keys requests by the given header, falling back to ClientIP.
func HeaderKey(header string) func(*http.Request) string {
	return func(r *http.Request) string {
		if v := r.Header.Get(header); v != "" {
			return v
		}
		return ClientIP(r)
	}
}

// New returns middleware that rejects over-limit requests with 429 and sets
// the IETF draft RateLimit-* headers on every response.
func New(o Options) func(http.Handler) http.Handler {
	if o.Backend == nil || o.Policy == nil {
		panic("middleware: Options.Backend and Options.Policy are required")
	}
	if o.Key == nil {
		o.Key = ClientIP
	}
	if o.Cost == nil {
		o.Cost = func(*http.Request) int64 { return 1 }
	}
	if o.OnError == nil {
		o.OnError = func(w http.ResponseWriter, r *http.Request, next http.Handler, _ error) { next.ServeHTTP(w, r) }
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rule, limit := o.Policy(r)
			d, err := o.Backend.Allow(r.Context(), rule+":"+o.Key(r), o.Cost(r), limit)
			if o.OnDecision != nil {
				o.OnDecision(r, rule, d, err)
			}
			if err != nil {
				o.OnError(w, r, next, err)
				return
			}
			SetHeaders(w.Header(), d, time.Now())
			if !d.Allowed {
				WriteDenied(w, d)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// SetHeaders writes RateLimit-Limit/Remaining/Reset and, when denied, Retry-After.
func SetHeaders(h http.Header, d limiter.Decision, now time.Time) {
	h.Set("RateLimit-Limit", strconv.FormatInt(d.Limit, 10))
	h.Set("RateLimit-Remaining", strconv.FormatInt(d.Remaining, 10))
	h.Set("RateLimit-Reset", strconv.FormatInt(ceilSeconds(d.ResetAt.Sub(now)), 10))
	if !d.Allowed {
		h.Set("Retry-After", strconv.FormatInt(max(ceilSeconds(d.RetryAfter), 1), 10))
	}
}

// WriteDenied writes a 429 JSON body describing the decision.
func WriteDenied(w http.ResponseWriter, d limiter.Decision) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusTooManyRequests)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error":          "rate limit exceeded",
		"retry_after_ms": d.RetryAfter.Milliseconds(),
	})
}

func ceilSeconds(d time.Duration) int64 {
	if d <= 0 {
		return 0
	}
	return int64(math.Ceil(d.Seconds()))
}
