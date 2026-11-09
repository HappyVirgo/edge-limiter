package limiter

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"
)

// FailureMode decides what happens when the primary backend is unavailable.
type FailureMode string

const (
	// FailOpen admits every request while the primary is down.
	FailOpen FailureMode = "open"
	// FailClosed denies every request while the primary is down.
	FailClosed FailureMode = "closed"
	// FailLocal enforces limits with a per-replica in-memory limiter.
	FailLocal FailureMode = "local"
)

// ParseFailureMode validates a configured failure mode.
func ParseFailureMode(s string) (FailureMode, error) {
	switch m := FailureMode(s); m {
	case FailOpen, FailClosed, FailLocal:
		return m, nil
	}
	return "", fmt.Errorf("limiter: unknown failure mode %q (want open, closed or local)", s)
}

// Failover wraps a primary (typically Redis) backend. When the primary
// errors it applies Mode, and it skips the primary for Cooldown afterwards so
// an outage does not add a network timeout to every request.
type Failover struct {
	Primary  Backend
	Local    Backend // required for FailLocal
	Mode     FailureMode
	Cooldown time.Duration
	Clock    Clock
	// OnFallback, if set, is called each time a decision is made without the
	// primary; err is the primary's error or ErrCircuitOpen.
	OnFallback func(mode FailureMode, err error)

	openUntil atomic.Int64 // unix nanoseconds
}

// ErrCircuitOpen marks decisions made while the primary is being skipped.
var ErrCircuitOpen = errors.New("limiter: primary backend skipped after recent failure")

// Name implements Backend.
func (f *Failover) Name() string { return f.Primary.Name() }

// Ping implements Pinger by pinging the primary if it supports it.
func (f *Failover) Ping(ctx context.Context) error {
	if p, ok := f.Primary.(Pinger); ok {
		return p.Ping(ctx)
	}
	return nil
}

// Degraded reports whether the primary is currently being bypassed.
func (f *Failover) Degraded() bool { return f.now().UnixNano() < f.openUntil.Load() }

// Allow implements Backend.
func (f *Failover) Allow(ctx context.Context, key string, cost int64, limit Limit) (Decision, error) {
	if err := checkArgs(cost, limit); err != nil {
		return Decision{}, err
	}
	if f.Degraded() {
		return f.fallback(ctx, key, cost, limit, ErrCircuitOpen)
	}
	d, err := f.Primary.Allow(ctx, key, cost, limit)
	if err == nil {
		return d, nil
	}
	if ctx.Err() != nil {
		return Decision{}, err // caller gave up; not a backend outage
	}
	f.openUntil.Store(f.now().Add(f.Cooldown).UnixNano())
	return f.fallback(ctx, key, cost, limit, err)
}

func (f *Failover) fallback(ctx context.Context, key string, cost int64, limit Limit, cause error) (Decision, error) {
	if f.OnFallback != nil {
		f.OnFallback(f.Mode, cause)
	}
	now := f.now()
	switch f.Mode {
	case FailOpen:
		return Decision{Allowed: true, Limit: limit.Capacity(), Remaining: limit.Capacity() - cost, ResetAt: now}, nil
	case FailLocal:
		if f.Local != nil {
			return f.Local.Allow(ctx, key, cost, limit)
		}
	}
	retry := max(f.Cooldown, time.Second)
	return Decision{Limit: limit.Capacity(), ResetAt: now.Add(retry), RetryAfter: retry}, nil
}

func (f *Failover) now() time.Time {
	if f.Clock == nil {
		return time.Now()
	}
	return f.Clock.Now()
}
