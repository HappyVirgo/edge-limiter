package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/HappyVirgo/edge-limiter/limiter"
)

var ok = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })

func do(h http.Handler, remote, tenant string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "/orders", nil)
	r.RemoteAddr = remote
	if tenant != "" {
		r.Header.Set("X-Tenant-ID", tenant)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestMiddlewareLimitsPerClientAndSetsHeaders(t *testing.T) {
	clk := limiter.NewManualClock(time.Now())
	l := limiter.Limit{Algorithm: limiter.SlidingWindowLog, Rate: 2, Period: 10 * time.Second}
	h := New(Options{Backend: limiter.NewMemory(limiter.WithClock(clk)), Policy: StaticPolicy("api", l)})(ok)

	for i := range 2 {
		w := do(h, "10.0.0.1:5000", "")
		if w.Code != http.StatusNoContent || w.Header().Get("RateLimit-Remaining") != fmt.Sprint(1-i) {
			t.Fatalf("request %d: code=%d headers=%v", i, w.Code, w.Header())
		}
	}
	w := do(h, "10.0.0.1:5001", "") // same IP, different port: same key
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("third request: code %d", w.Code)
	}
	if got := w.Header().Get("Retry-After"); got != "10" {
		t.Fatalf("Retry-After = %q, want 10", got)
	}
	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil || body["retry_after_ms"] != float64(10000) {
		t.Fatalf("body %v err %v", body, err)
	}
	if w := do(h, "10.0.0.2:5000", ""); w.Code != http.StatusNoContent {
		t.Fatalf("other client was limited: %d", w.Code)
	}
}

func TestMiddlewareHeaderKeyAndCost(t *testing.T) {
	l := limiter.Limit{Algorithm: limiter.TokenBucket, Rate: 10, Period: time.Minute}
	var seen []string
	h := New(Options{
		Backend: limiter.NewMemory(),
		Policy:  StaticPolicy("api", l),
		Key:     HeaderKey("X-Tenant-ID"),
		Cost:    func(*http.Request) int64 { return 4 },
		OnDecision: func(_ *http.Request, rule string, d limiter.Decision, _ error) {
			seen = append(seen, fmt.Sprintf("%s:%v", rule, d.Allowed))
		},
	})(ok)
	// Two different IPs share the tenant's budget of 10 units at 4 per request.
	codes := []int{do(h, "1.1.1.1:1", "acme").Code, do(h, "2.2.2.2:1", "acme").Code, do(h, "3.3.3.3:1", "acme").Code}
	if codes[0] != 204 || codes[1] != 204 || codes[2] != 429 {
		t.Fatalf("codes %v", codes)
	}
	if do(h, "3.3.3.3:1", "").Code != 204 {
		t.Fatal("request without tenant should fall back to its IP key")
	}
	if len(seen) != 4 || seen[2] != "api:false" {
		t.Fatalf("OnDecision calls: %v", seen)
	}
}

type brokenBackend struct{}

func (brokenBackend) Name() string { return "broken" }
func (brokenBackend) Allow(context.Context, string, int64, limiter.Limit) (limiter.Decision, error) {
	return limiter.Decision{}, errors.New("redis: connection refused")
}

func TestMiddlewareBackendErrors(t *testing.T) {
	l := limiter.Limit{Algorithm: limiter.TokenBucket, Rate: 1, Period: time.Second}
	open := New(Options{Backend: brokenBackend{}, Policy: StaticPolicy("api", l)})(ok)
	if w := do(open, "1.1.1.1:1", ""); w.Code != http.StatusNoContent {
		t.Fatalf("default OnError should fail open, got %d", w.Code)
	}
	closed := New(Options{Backend: brokenBackend{}, Policy: StaticPolicy("api", l),
		OnError: func(w http.ResponseWriter, _ *http.Request, _ http.Handler, _ error) {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
		}})(ok)
	if w := do(closed, "1.1.1.1:1", ""); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("custom OnError not used, got %d", w.Code)
	}
}

func TestMiddlewareWithFailoverFailsClosed(t *testing.T) {
	l := limiter.Limit{Algorithm: limiter.TokenBucket, Rate: 1, Period: time.Second}
	f := &limiter.Failover{Primary: brokenBackend{}, Mode: limiter.FailClosed, Cooldown: time.Second}
	h := New(Options{Backend: f, Policy: StaticPolicy("api", l)})(ok)
	if w := do(h, "1.1.1.1:1", ""); w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") != "1" {
		t.Fatalf("fail-closed: code=%d headers=%v", w.Code, w.Header())
	}
}

func ExampleNew() {
	limit := limiter.Limit{Algorithm: limiter.TokenBucket, Rate: 5, Period: time.Second, Burst: 10}
	mw := New(Options{Backend: limiter.NewMemory(), Policy: StaticPolicy("api", limit)})
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprintln(w, "hello") })
	srv := httptest.NewServer(mw(mux))
	defer srv.Close()

	res, _ := http.Get(srv.URL)
	fmt.Println(res.StatusCode, res.Header.Get("RateLimit-Limit"), res.Header.Get("RateLimit-Remaining"))
	// Output: 200 10 9
}
