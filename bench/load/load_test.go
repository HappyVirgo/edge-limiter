package load

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunCountsDecisionsAndPercentiles(t *testing.T) {
	var n atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/check" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		if n.Add(1)%2 == 0 {
			_, _ = w.Write([]byte(`{"allowed":false}`))
			return
		}
		_, _ = w.Write([]byte(`{"allowed":true}`))
	}))
	defer srv.Close()

	r := Run(context.Background(), Options{URL: srv.URL, Concurrency: 4, Duration: 300 * time.Millisecond, Keys: 10})
	if r.Checks == 0 || r.Errors != 0 || r.Allowed+r.Denied != r.Checks {
		t.Fatalf("unexpected counts: %s", r)
	}
	if r.Denied == 0 || r.Allowed == 0 {
		t.Fatalf("expected both allowed and denied responses: %s", r)
	}
	if !(r.P50 <= r.P95 && r.P95 <= r.P99 && r.P99 <= r.Max) || r.ChecksPerSec() <= 0 {
		t.Fatalf("percentiles out of order: %s", r)
	}
}
