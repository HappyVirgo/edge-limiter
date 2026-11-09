package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"

	"github.com/HappyVirgo/edge-limiter/internal/config"
	"github.com/HappyVirgo/edge-limiter/internal/metrics"
)

const testConfig = `
backend: {type: memory}
default: {algorithm: token_bucket, rate: 3, period: 1m}
rules:
  - {name: login, route: /login, algorithm: sliding_window_log, rate: 1, period: 1m}
`

func newTestServer(t *testing.T, doc string) (*Server, http.Handler) {
	t.Helper()
	cfg, err := config.Parse([]byte(doc), func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	m := metrics.New()
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	backend, closer := BuildBackend(cfg, m, log)
	t.Cleanup(func() { _ = closer.Close() })
	s := New(cfg, backend, m, log)
	return s, s.Handler()
}

func call(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
	return w
}

func TestCheckEndpoint(t *testing.T) {
	_, h := newTestServer(t, testConfig)
	var got []CheckResponse
	for range 4 {
		w := call(h, "POST", "/v1/check", `{"key":"user-1","cost":1}`)
		if w.Code != http.StatusOK {
			t.Fatalf("status %d: %s", w.Code, w.Body)
		}
		var r CheckResponse
		if err := json.Unmarshal(w.Body.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	if !got[2].Allowed || got[2].Remaining != 0 || got[3].Allowed || got[3].RetryAfter < 19 || got[3].Rule != "default" {
		t.Fatalf("unexpected decisions: %+v", got)
	}
	// Route selects the stricter login rule; keys are namespaced per rule.
	w := call(h, "POST", "/v1/check", `{"key":"user-1","route":"/login"}`)
	if !strings.Contains(w.Body.String(), `"rule":"login"`) || !strings.Contains(w.Body.String(), `"allowed":true`) {
		t.Fatalf("login rule: %s", w.Body)
	}
}

func TestCheckValidation(t *testing.T) {
	_, h := newTestServer(t, testConfig)
	cases := map[string]string{
		"not json":      `{`,
		"missing key":   `{"cost":1}`,
		"unknown field": `{"key":"a","weight":2}`,
		"cost too high": `{"key":"a","cost":4}`,
		"negative cost": `{"key":"a","cost":-1}`,
		"key too long":  `{"key":"` + strings.Repeat("k", 257) + `"}`,
	}
	for name, body := range cases {
		if w := call(h, "POST", "/v1/check", body); w.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, w.Code)
		}
	}
	if w := call(h, "GET", "/v1/check", ""); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /v1/check: %d", w.Code)
	}
}

func TestReadinessAndShutdown(t *testing.T) {
	s, h := newTestServer(t, testConfig)
	if w := call(h, "GET", "/readyz", ""); w.Code != http.StatusOK {
		t.Fatalf("readyz %d", w.Code)
	}
	s.SetReady(false)
	if w := call(h, "GET", "/readyz", ""); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz during shutdown %d", w.Code)
	}
	if w := call(h, "GET", "/healthz", ""); w.Code != http.StatusOK {
		t.Fatalf("liveness must stay up during shutdown: %d", w.Code)
	}
}

func TestRedisDownReadinessDependsOnFailureMode(t *testing.T) {
	mr := miniredis.RunT(t)
	addr := mr.Addr()
	mr.Close()
	for mode, want := range map[string]int{"closed": 503, "local": 200, "open": 200} {
		_, h := newTestServer(t, "backend: {type: redis, failure_mode: "+mode+", redis: {addr: '"+addr+"'}}")
		if w := call(h, "GET", "/readyz", ""); w.Code != want {
			t.Errorf("%s: readyz %d, want %d (%s)", mode, w.Code, want, w.Body)
		}
		// The check API itself keeps answering according to the mode.
		w := call(h, "POST", "/v1/check", `{"key":"k"}`)
		allowed := strings.Contains(w.Body.String(), `"allowed":true`)
		if w.Code != http.StatusOK || allowed != (mode != "closed") {
			t.Errorf("%s: check %d %s", mode, w.Code, w.Body)
		}
	}
}

func TestMetricsExposed(t *testing.T) {
	_, h := newTestServer(t, testConfig)
	call(h, "POST", "/v1/check", `{"key":"a"}`)
	body := call(h, "GET", "/metrics", "").Body.String()
	for _, want := range []string{
		`edge_limiter_checks_total{result="allowed",rule="default"} 1`,
		`edge_limiter_http_requests_total{code="200",handler="check"} 1`,
		`edge_limiter_backend_latency_seconds_count{backend="memory",outcome="ok"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics missing %q", want)
		}
	}
}
