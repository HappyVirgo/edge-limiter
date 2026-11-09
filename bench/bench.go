// Package bench holds the micro-benchmarks and the accuracy simulation shared
// by `go test -bench ./bench` and the report generator in bench/cmd/report.
package bench

import (
	"context"
	"math/rand/v2"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/HappyVirgo/edge-limiter/limiter"
)

// Limits are the configurations exercised by every benchmark: 100 units per
// second, token bucket burst equal to the rate.
var Limits = []limiter.Limit{
	{Algorithm: limiter.TokenBucket, Rate: 100, Period: time.Second, Burst: 100},
	{Algorithm: limiter.SlidingWindowLog, Rate: 100, Period: time.Second},
	{Algorithm: limiter.SlidingWindowCounter, Rate: 100, Period: time.Second},
}

// BackendFactory builds a fresh backend on clk and returns a cleanup func.
type BackendFactory struct {
	Name string
	New  func(clk limiter.Clock) (limiter.Backend, func())
}

// Factories lists the backends under test. "miniredis" runs the real Lua
// scripts through go-redis against an in-process Redis emulator.
var Factories = []BackendFactory{
	{Name: "memory", New: func(clk limiter.Clock) (limiter.Backend, func()) {
		return limiter.NewMemory(limiter.WithClock(clk)), func() {}
	}},
	{Name: "miniredis", New: func(clk limiter.Clock) (limiter.Backend, func()) {
		mr, err := miniredis.Run()
		if err != nil {
			panic(err)
		}
		c := redis.NewClient(&redis.Options{Addr: mr.Addr()})
		return limiter.NewRedis(c, "bench:", clk), func() { _ = c.Close(); mr.Close() }
	}},
}

// AllowBenchmark returns a parallel benchmark of Allow over 1024 keys.
func AllowBenchmark(f BackendFactory, l limiter.Limit) func(*testing.B) {
	return func(b *testing.B) {
		backend, cleanup := f.New(limiter.SystemClock{})
		defer cleanup()
		keys := make([]string, 1024)
		for i := range keys {
			keys[i] = "user-" + strconv.Itoa(i)
		}
		ctx := context.Background()
		b.ReportAllocs()
		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			i := rand.IntN(len(keys))
			for pb.Next() {
				if _, err := backend.Allow(ctx, keys[i&1023], 1, l); err != nil {
					b.Error(err)
					return
				}
				i++
			}
		})
	}
}

// BurstyTraffic returns sorted synthetic arrival offsets: a Poisson
// background of baseRPS plus bursts of burstSize requests squeezed into
// 200ms, arriving on average every burstEvery. Seeded, so reproducible.
func BurstyTraffic(seed uint64, duration time.Duration, baseRPS float64, burstSize int, burstEvery time.Duration) []time.Duration {
	rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	var out []time.Duration
	for t := time.Duration(0); ; {
		t += time.Duration(rng.ExpFloat64() / baseRPS * float64(time.Second))
		if t >= duration {
			break
		}
		out = append(out, t)
	}
	for t := time.Duration(0); ; {
		t += time.Duration(rng.ExpFloat64() * float64(burstEvery))
		if t >= duration {
			break
		}
		for range burstSize {
			out = append(out, t+time.Duration(rng.Int64N(int64(200*time.Millisecond))))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// AccuracyResult summarises how closely a limiter tracked its limit.
type AccuracyResult struct {
	Offered, Allowed int
	// MaxInWindow is the most units admitted in any trailing Period window.
	MaxInWindow int
	// AllowedPerPeriod is the mean admitted rate, in units per Period.
	AllowedPerPeriod float64
}

// Accuracy replays arrivals for a single key against backend on a manual
// clock and measures the admitted traffic.
func Accuracy(f BackendFactory, l limiter.Limit, arrivals []time.Duration, duration time.Duration) AccuracyResult {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clk := limiter.NewManualClock(start)
	backend, cleanup := f.New(clk)
	defer cleanup()

	var admitted []time.Duration
	var elapsed time.Duration
	for _, at := range arrivals {
		clk.Advance(at - elapsed)
		elapsed = at
		d, err := backend.Allow(context.Background(), "tenant-1", 1, l)
		if err != nil {
			panic(err)
		}
		if d.Allowed {
			admitted = append(admitted, at)
		}
	}
	maxWin, lo := 0, 0
	for hi, t := range admitted {
		for admitted[lo] <= t-l.Period { // same expiry rule as the limiter
			lo++
		}
		maxWin = max(maxWin, hi-lo+1)
	}
	return AccuracyResult{
		Offered:          len(arrivals),
		Allowed:          len(admitted),
		MaxInWindow:      maxWin,
		AllowedPerPeriod: float64(len(admitted)) / (float64(duration) / float64(l.Period)),
	}
}
