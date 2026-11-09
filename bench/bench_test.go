package bench

import (
	"testing"
	"time"

	"github.com/HappyVirgo/edge-limiter/limiter"
)

func BenchmarkAllow(b *testing.B) {
	for _, f := range Factories {
		for _, l := range Limits {
			b.Run(f.Name+"/"+string(l.Algorithm), AllowBenchmark(f, l))
		}
	}
}

// TestAccuracyUnderBurstyTraffic checks each algorithm's worst-case trailing
// window against its theoretical bound, and that both backends agree.
func TestAccuracyUnderBurstyTraffic(t *testing.T) {
	const dur = 30 * time.Second
	arrivals := BurstyTraffic(42, dur, 60, 400, 3*time.Second)
	for _, l := range Limits {
		var results []AccuracyResult
		for _, f := range Factories {
			results = append(results, Accuracy(f, l, arrivals, dur))
		}
		if results[0] != results[1] {
			t.Fatalf("%s: memory %+v != miniredis %+v", l.Algorithm, results[0], results[1])
		}
		r := results[0]
		var bound int64
		switch l.Algorithm {
		case limiter.SlidingWindowLog:
			bound = l.Rate // exact
		case limiter.TokenBucket:
			bound = l.Rate + l.Burst // a full bucket plus one period of refill
		case limiter.SlidingWindowCounter:
			bound = l.Rate * 2 // loose theoretical worst case; observed is far lower
		}
		if int64(r.MaxInWindow) > bound {
			t.Errorf("%s: max %d units in a window, bound %d", l.Algorithm, r.MaxInWindow, bound)
		}
		if r.AllowedPerPeriod > float64(l.Rate)*1.05 {
			t.Errorf("%s: long-run rate %.1f exceeds limit %d by >5%%", l.Algorithm, r.AllowedPerPeriod, l.Rate)
		}
	}
}
