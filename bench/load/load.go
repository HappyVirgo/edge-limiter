// Package load drives POST /v1/check with concurrent workers and reports
// throughput and latency percentiles.
package load

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"time"
)

// Options configures a load run.
type Options struct {
	URL         string // base URL, e.g. http://127.0.0.1:8080
	Concurrency int
	Duration    time.Duration
	Keys        int // distinct keys, chosen uniformly at random
}

// Result summarises a load run.
type Result struct {
	Checks, Allowed, Denied, Errors int
	Elapsed                         time.Duration
	P50, P95, P99, Max              time.Duration
}

// ChecksPerSec is successful checks divided by wall time.
func (r Result) ChecksPerSec() float64 { return float64(r.Checks) / r.Elapsed.Seconds() }

// String formats the result as one line.
func (r Result) String() string {
	return fmt.Sprintf("checks=%d (%.0f/s) allowed=%d denied=%d errors=%d p50=%s p95=%s p99=%s max=%s",
		r.Checks, r.ChecksPerSec(), r.Allowed, r.Denied, r.Errors, r.P50, r.P95, r.P99, r.Max)
}

// Run hammers the check endpoint until o.Duration elapses or ctx is done.
func Run(ctx context.Context, o Options) Result {
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        o.Concurrency,
			MaxIdleConnsPerHost: o.Concurrency,
		},
	}
	defer client.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(ctx, o.Duration)
	defer cancel()

	type workerStats struct {
		lat                     []time.Duration
		allowed, denied, errors int
	}
	stats := make([]workerStats, o.Concurrency)
	var wg sync.WaitGroup
	start := time.Now()
	for w := range o.Concurrency {
		wg.Go(func() {
			s := &stats[w]
			var resp struct {
				Allowed bool `json:"allowed"`
			}
			for ctx.Err() == nil {
				body := []byte(`{"key":"k` + strconv.Itoa(rand.IntN(o.Keys)) + `"}`)
				t0 := time.Now()
				ok := check(ctx, client, o.URL, body, &resp)
				if ctx.Err() != nil {
					return // do not count requests cut off by the deadline
				}
				switch {
				case !ok:
					s.errors++
					continue
				case resp.Allowed:
					s.allowed++
				default:
					s.denied++
				}
				s.lat = append(s.lat, time.Since(t0))
			}
		})
	}
	wg.Wait()

	var r Result
	r.Elapsed = time.Since(start)
	var all []time.Duration
	for _, s := range stats {
		all = append(all, s.lat...)
		r.Allowed += s.allowed
		r.Denied += s.denied
		r.Errors += s.errors
	}
	r.Checks = len(all)
	if len(all) > 0 {
		slices.Sort(all)
		pct := func(p float64) time.Duration { return all[int(p*float64(len(all)-1))] }
		r.P50, r.P95, r.P99, r.Max = pct(.50), pct(.95), pct(.99), all[len(all)-1]
	}
	return r
}

func check(ctx context.Context, c *http.Client, url string, body []byte, out any) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url+"/v1/check", bytes.NewReader(body))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := c.Do(req)
	if err != nil {
		return false
	}
	defer res.Body.Close()
	decodeErr := json.NewDecoder(res.Body).Decode(out)
	_, _ = io.Copy(io.Discard, res.Body) // drain so the connection is reused
	return res.StatusCode == http.StatusOK && decodeErr == nil
}
