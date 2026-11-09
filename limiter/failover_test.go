package limiter

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// downRedis returns a Redis backend whose server has been stopped.
func downRedis(t *testing.T, clk Clock) *Redis {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr(), MaxRetries: -1, DialTimeout: 50 * time.Millisecond})
	t.Cleanup(func() { _ = client.Close() })
	mr.Close()
	return NewRedis(client, "", clk)
}

func TestFailoverModes(t *testing.T) {
	l := Limit{Algorithm: TokenBucket, Rate: 2, Period: time.Minute}
	cases := []struct {
		mode        FailureMode
		wantAllowed []bool
	}{
		{FailOpen, []bool{true, true, true}},
		{FailClosed, []bool{false, false, false}},
		{FailLocal, []bool{true, true, false}}, // local limiter enforces rate=2
	}
	for _, c := range cases {
		t.Run(string(c.mode), func(t *testing.T) {
			clk := NewManualClock(epoch)
			var fallbacks int
			f := &Failover{
				Primary:    downRedis(t, clk),
				Local:      NewMemory(WithClock(clk)),
				Mode:       c.mode,
				Cooldown:   time.Second,
				Clock:      clk,
				OnFallback: func(FailureMode, error) { fallbacks++ },
			}
			for i, want := range c.wantAllowed {
				d, err := f.Allow(context.Background(), "k", 1, l)
				if err != nil {
					t.Fatalf("fallback must not surface backend errors: %v", err)
				}
				if d.Allowed != want {
					t.Fatalf("request %d: allowed=%v want %v", i, d.Allowed, want)
				}
				if !d.Allowed && d.RetryAfter <= 0 {
					t.Fatalf("denied without retry hint: %+v", d)
				}
			}
			if fallbacks != 3 || !f.Degraded() {
				t.Fatalf("fallbacks=%d degraded=%v", fallbacks, f.Degraded())
			}
		})
	}
}

// flakyBackend fails while down is true and counts calls.
type flakyBackend struct {
	down  bool
	calls int
}

func (b *flakyBackend) Name() string { return "flaky" }
func (b *flakyBackend) Allow(context.Context, string, int64, Limit) (Decision, error) {
	b.calls++
	if b.down {
		return Decision{}, errors.New("connection refused")
	}
	return Decision{Allowed: true}, nil
}

func TestFailoverCooldownSkipsPrimaryThenRecovers(t *testing.T) {
	clk := NewManualClock(epoch)
	primary := &flakyBackend{down: true}
	var causes []error
	f := &Failover{Primary: primary, Mode: FailOpen, Cooldown: time.Second, Clock: clk,
		OnFallback: func(_ FailureMode, err error) { causes = append(causes, err) }}
	l := Limit{Algorithm: SlidingWindowCounter, Rate: 1, Period: time.Second}

	for range 5 {
		if _, err := f.Allow(context.Background(), "k", 1, l); err != nil {
			t.Fatal(err)
		}
	}
	if primary.calls != 1 {
		t.Fatalf("primary called %d times during cooldown, want 1", primary.calls)
	}
	if !errors.Is(causes[1], ErrCircuitOpen) {
		t.Fatalf("second fallback cause = %v, want ErrCircuitOpen", causes[1])
	}
	primary.down = false
	clk.Advance(time.Second)
	if _, err := f.Allow(context.Background(), "k", 1, l); err != nil || primary.calls != 2 || f.Degraded() {
		t.Fatalf("did not return to primary after cooldown: err=%v calls=%d", err, primary.calls)
	}
}

func TestFailoverCancelledContextIsNotAnOutage(t *testing.T) {
	f := &Failover{Primary: &flakyBackend{down: true}, Mode: FailOpen, Cooldown: time.Second}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	l := Limit{Algorithm: TokenBucket, Rate: 1, Period: time.Second}
	if _, err := f.Allow(ctx, "k", 1, l); err == nil || f.Degraded() {
		t.Fatalf("cancelled request should error without tripping the breaker: err=%v", err)
	}
}

func TestParseFailureMode(t *testing.T) {
	if _, err := ParseFailureMode("sometimes"); err == nil {
		t.Fatal("expected error")
	}
	if m, err := ParseFailureMode("local"); err != nil || m != FailLocal {
		t.Fatalf("got %v %v", m, err)
	}
}
