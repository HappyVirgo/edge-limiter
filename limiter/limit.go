// Package limiter implements rate-limiting algorithms (token bucket via GCRA,
// sliding-window log, sliding-window counter) over pluggable storage backends.
//
// All algorithms work in integer microseconds so that the in-memory backend
// and the Redis Lua scripts perform the same IEEE-754 operations in the same
// order and therefore make identical decisions for identical inputs.
package limiter

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Algorithm selects how a Limit is enforced.
type Algorithm string

const (
	// TokenBucket is a token bucket expressed as GCRA: Rate tokens refill per
	// Period and up to Burst tokens may be spent at once.
	TokenBucket Algorithm = "token_bucket"
	// SlidingWindowLog records every admitted unit and allows at most Rate
	// units in any trailing window of length Period. Exact but O(Rate) memory.
	SlidingWindowLog Algorithm = "sliding_window_log"
	// SlidingWindowCounter approximates a sliding window by weighting the
	// previous fixed window's count. O(1) memory, small approximation error.
	SlidingWindowCounter Algorithm = "sliding_window_counter"
)

// Errors returned by Validate and backends.
var (
	ErrInvalidLimit = errors.New("limiter: invalid limit")
	ErrInvalidCost  = errors.New("limiter: cost must be >= 1 and <= limit capacity")
)

// Limit describes how many units a key may consume.
type Limit struct {
	Algorithm Algorithm     `yaml:"algorithm" json:"algorithm"`
	Rate      int64         `yaml:"rate" json:"rate"`     // units per Period
	Period    time.Duration `yaml:"period" json:"period"` // refill / window length
	Burst     int64         `yaml:"burst" json:"burst"`   // token bucket only; defaults to Rate
}

// Capacity is the largest cost a single request may ever be granted.
func (l Limit) Capacity() int64 {
	if l.Algorithm == TokenBucket && l.Burst > 0 {
		return l.Burst
	}
	return l.Rate
}

// Validate reports whether the limit is usable.
func (l Limit) Validate() error {
	switch l.Algorithm {
	case TokenBucket, SlidingWindowLog, SlidingWindowCounter:
	default:
		return fmt.Errorf("%w: unknown algorithm %q", ErrInvalidLimit, l.Algorithm)
	}
	if l.Rate <= 0 || l.Burst < 0 {
		return fmt.Errorf("%w: rate must be > 0 and burst >= 0", ErrInvalidLimit)
	}
	if l.Period < time.Millisecond {
		return fmt.Errorf("%w: period must be >= 1ms", ErrInvalidLimit)
	}
	return nil
}

func (l Limit) periodMicros() int64 { return l.Period.Microseconds() }

func (l Limit) burst() int64 {
	if l.Burst > 0 {
		return l.Burst
	}
	return l.Rate
}

// Decision is the outcome of a single check.
type Decision struct {
	Allowed    bool
	Limit      int64         // capacity of the limit that was applied
	Remaining  int64         // units still available right now
	ResetAt    time.Time     // when the key returns to full capacity
	RetryAfter time.Duration // zero when allowed; otherwise wait at least this long
}

// Backend stores limiter state and makes decisions atomically per key.
type Backend interface {
	// Allow consumes cost units from key under limit if they are available.
	Allow(ctx context.Context, key string, cost int64, limit Limit) (Decision, error)
	// Name identifies the backend in metrics and logs.
	Name() string
}

// Pinger is implemented by backends that depend on a remote service.
type Pinger interface {
	Ping(ctx context.Context) error
}

func checkArgs(cost int64, limit Limit) error {
	if err := limit.Validate(); err != nil {
		return err
	}
	if cost < 1 || cost > limit.Capacity() {
		return ErrInvalidCost
	}
	return nil
}

// result is the backend-neutral decision in microseconds.
type result struct {
	allowed   bool
	remaining int64
	resetUS   int64 // absolute unix microseconds
	retryUS   int64 // relative microseconds
}

func (r result) decision(limit Limit) Decision {
	return Decision{
		Allowed:    r.allowed,
		Limit:      limit.Capacity(),
		Remaining:  r.remaining,
		ResetAt:    time.UnixMicro(r.resetUS),
		RetryAfter: time.Duration(r.retryUS) * time.Microsecond,
	}
}
