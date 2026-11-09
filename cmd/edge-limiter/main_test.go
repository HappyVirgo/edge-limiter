package main

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncBuffer is a goroutine-safe log sink.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

func TestRunServesAndShutsDownGracefully(t *testing.T) {
	addr := freeAddr(t)
	env := map[string]string{"EDGE_LIMITER_LISTEN": addr}
	var logs syncBuffer
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{"-config", "../../configs/local.yaml"}, func(k string) string { return env[k] }, &logs)
	}()

	var res *http.Response
	var err error
	for range 50 {
		if res, err = http.Post("http://"+addr+"/v1/check", "application/json", strings.NewReader(`{"key":"a"}`)); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("server never came up: %v", err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("check status %d", res.StatusCode)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown timed out")
	}
	for _, want := range []string{`"msg":"listening"`, `"msg":"shutting down"`, `"msg":"stopped"`} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("logs missing %s:\n%s", want, logs.String())
		}
	}
}

func TestRunRejectsBadConfig(t *testing.T) {
	err := run(context.Background(), []string{"-config", "does-not-exist.yaml"}, func(string) string { return "" }, &syncBuffer{})
	if err == nil {
		t.Fatal("expected error for missing config file")
	}
}
