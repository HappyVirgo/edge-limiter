package limiter

import (
	"context"
	"errors"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

var epoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// backends returns a memory and a miniredis-backed Redis backend sharing clk.
func backends(t *testing.T, clk Clock) map[string]Backend {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return map[string]Backend{
		"memory": NewMemory(WithClock(clk)),
		"redis":  NewRedis(client, "test:", clk),
	}
}

// eachBackend runs fn once per backend, each with a fresh clock at epoch.
func eachBackend(t *testing.T, fn func(t *testing.T, b Backend, clk *ManualClock)) {
	for _, name := range []string{"memory", "redis"} {
		t.Run(name, func(t *testing.T) {
			clk := NewManualClock(epoch)
			fn(t, backends(t, clk)[name], clk)
		})
	}
}

func mustAllow(t *testing.T, b Backend, key string, cost int64, l Limit) Decision {
	t.Helper()
	d, err := b.Allow(context.Background(), key, cost, l)
	if err != nil {
		t.Fatalf("Allow: %v", err)
	}
	return d
}

func TestTokenBucket(t *testing.T) {
	l := Limit{Algorithm: TokenBucket, Rate: 10, Period: time.Second, Burst: 5}
	eachBackend(t, func(t *testing.T, b Backend, clk *ManualClock) {
		for i := range 5 {
			d := mustAllow(t, b, "k", 1, l)
			if !d.Allowed || d.Remaining != int64(4-i) {
				t.Fatalf("request %d: %+v", i, d)
			}
		}
		d := mustAllow(t, b, "k", 1, l)
		if d.Allowed || d.RetryAfter != 100*time.Millisecond || d.Remaining != 0 {
			t.Fatalf("6th request should wait one interval: %+v", d)
		}
		if want := epoch.Add(500 * time.Millisecond); !d.ResetAt.Equal(want) {
			t.Fatalf("reset %v, want %v", d.ResetAt, want)
		}
		clk.Advance(100 * time.Millisecond)
		if d := mustAllow(t, b, "k", 1, l); !d.Allowed || d.Remaining != 0 {
			t.Fatalf("refilled token not granted: %+v", d)
		}
		clk.Advance(time.Second) // bucket fully refills, never above burst
		if d := mustAllow(t, b, "k", 5, l); !d.Allowed || d.Remaining != 0 {
			t.Fatalf("full burst not granted: %+v", d)
		}
	})
}

func TestSlidingWindowLog(t *testing.T) {
	l := Limit{Algorithm: SlidingWindowLog, Rate: 3, Period: time.Second}
	eachBackend(t, func(t *testing.T, b Backend, clk *ManualClock) {
		mustAllow(t, b, "k", 1, l)          // t=0
		clk.Advance(400 * time.Millisecond) // t=400
		mustAllow(t, b, "k", 2, l)          // t=400, window full
		clk.Advance(100 * time.Millisecond) // t=500
		d := mustAllow(t, b, "k", 2, l)     // needs both t=0 and t=400 units to expire
		if d.Allowed || d.Remaining != 0 || d.RetryAfter != 900*time.Millisecond {
			t.Fatalf("expected deny with retry 900ms: %+v", d)
		}
		if want := epoch.Add(1400 * time.Millisecond); !d.ResetAt.Equal(want) {
			t.Fatalf("reset %v, want %v", d.ResetAt, want)
		}
		clk.Advance(500 * time.Millisecond) // t=1000: the t=0 unit expires
		if d := mustAllow(t, b, "k", 1, l); !d.Allowed || d.Remaining != 0 {
			t.Fatalf("expired unit not reclaimed: %+v", d)
		}
	})
}

func TestSlidingWindowCounter(t *testing.T) {
	l := Limit{Algorithm: SlidingWindowCounter, Rate: 10, Period: time.Second}
	// epoch is aligned to a window boundary.
	eachBackend(t, func(t *testing.T, b Backend, clk *ManualClock) {
		if d := mustAllow(t, b, "k", 10, l); !d.Allowed || d.Remaining != 0 {
			t.Fatalf("first window: %+v", d)
		}
		clk.Advance(1250 * time.Millisecond) // 25% into next window: est = 10*0.75 = 7.5
		d := mustAllow(t, b, "k", 2, l)
		if !d.Allowed || d.Remaining != 0 {
			t.Fatalf("7.5+2 <= 10 should pass: %+v", d)
		}
		d = mustAllow(t, b, "k", 2, l) // est = 9.5; needs prev weight <= 6/10 -> at 40%
		if d.Allowed || d.RetryAfter != 150*time.Millisecond {
			t.Fatalf("expected deny with 150ms retry: %+v", d)
		}
		clk.Advance(150 * time.Millisecond)
		if d := mustAllow(t, b, "k", 2, l); !d.Allowed {
			t.Fatalf("retry_after was not sufficient: %+v", d)
		}
	})
}

func TestInvalidArguments(t *testing.T) {
	b := NewMemory()
	good := Limit{Algorithm: TokenBucket, Rate: 5, Period: time.Second}
	cases := []struct {
		cost int64
		l    Limit
		want error
	}{
		{0, good, ErrInvalidCost},
		{6, good, ErrInvalidCost},
		{1, Limit{Algorithm: "leaky", Rate: 1, Period: time.Second}, ErrInvalidLimit},
		{1, Limit{Algorithm: TokenBucket, Rate: 0, Period: time.Second}, ErrInvalidLimit},
		{1, Limit{Algorithm: TokenBucket, Rate: 1, Period: time.Microsecond}, ErrInvalidLimit},
	}
	for _, c := range cases {
		if _, err := b.Allow(context.Background(), "k", c.cost, c.l); !errors.Is(err, c.want) {
			t.Errorf("cost=%d limit=%+v: err=%v, want %v", c.cost, c.l, err, c.want)
		}
	}
}

// TestBackendsAgree replays the same random traffic against both backends and
// requires bit-identical decisions, proving the Lua scripts mirror the Go code.
func TestBackendsAgree(t *testing.T) {
	limits := []Limit{
		{Algorithm: TokenBucket, Rate: 7, Period: time.Second, Burst: 3},
		{Algorithm: TokenBucket, Rate: 1000, Period: time.Minute},
		{Algorithm: SlidingWindowLog, Rate: 9, Period: 700 * time.Millisecond},
		{Algorithm: SlidingWindowCounter, Rate: 13, Period: 900 * time.Millisecond},
	}
	for _, l := range limits {
		clk := NewManualClock(epoch.Add(123456 * time.Microsecond))
		bs := backends(t, clk)
		rng := rand.New(rand.NewPCG(1, uint64(l.Rate)))
		for i := range 2000 {
			key := []string{"a", "b"}[rng.IntN(2)]
			cost := 1 + rng.Int64N(min(l.Capacity(), 3))
			dm := mustAllow(t, bs["memory"], key, cost, l)
			dr := mustAllow(t, bs["redis"], key, cost, l)
			if dm != dr {
				t.Fatalf("%s step %d diverged:\nmemory %+v\nredis  %+v", l.Algorithm, i, dm, dr)
			}
			clk.Advance(time.Duration(rng.Int64N(int64(l.Period / 5))))
		}
	}
}

func TestMemoryConcurrentNeverOverAdmits(t *testing.T) {
	for _, alg := range []Algorithm{TokenBucket, SlidingWindowLog, SlidingWindowCounter} {
		t.Run(string(alg), func(t *testing.T) {
			l := Limit{Algorithm: alg, Rate: 500, Period: time.Hour}
			b := NewMemory(WithClock(NewManualClock(epoch)), WithShards(4))
			var allowed atomic.Int64
			var wg sync.WaitGroup
			for range 32 {
				wg.Go(func() {
					for range 100 {
						if d, _ := b.Allow(context.Background(), "hot", 1, l); d.Allowed {
							allowed.Add(1)
						}
					}
				})
			}
			wg.Wait()
			if got := allowed.Load(); got != 500 {
				t.Fatalf("allowed %d of 3200 concurrent requests, want exactly 500", got)
			}
		})
	}
}

func TestRedisConcurrentNeverOverAdmits(t *testing.T) {
	clk := NewManualClock(epoch)
	b := backends(t, clk)["redis"]
	l := Limit{Algorithm: SlidingWindowLog, Rate: 100, Period: time.Hour}
	var allowed atomic.Int64
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 50 {
				if mustAllow(t, b, "hot", 1, l).Allowed {
					allowed.Add(1)
				}
			}
		})
	}
	wg.Wait()
	if got := allowed.Load(); got != 100 {
		t.Fatalf("allowed %d, want 100", got)
	}
}

func TestMemorySweep(t *testing.T) {
	clk := NewManualClock(epoch)
	b := NewMemory(WithClock(clk))
	l := Limit{Algorithm: SlidingWindowCounter, Rate: 5, Period: time.Second}
	for _, k := range []string{"a", "b", "c"} {
		mustAllow(t, b, k, 1, l)
	}
	if n := b.Sweep(); n != 0 || b.Len() != 3 {
		t.Fatalf("swept live keys: removed=%d len=%d", n, b.Len())
	}
	clk.Advance(2 * time.Second)
	if n := b.Sweep(); n != 3 || b.Len() != 0 {
		t.Fatalf("expired keys not swept: removed=%d len=%d", n, b.Len())
	}
}
