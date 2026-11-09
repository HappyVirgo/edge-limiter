// Command edge-limiter runs the rate-limiting HTTP service.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/HappyVirgo/edge-limiter/internal/config"
	"github.com/HappyVirgo/edge-limiter/internal/metrics"
	"github.com/HappyVirgo/edge-limiter/internal/server"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Getenv, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "edge-limiter:", err)
		os.Exit(1)
	}
}

// run serves until ctx is cancelled, then drains in-flight requests.
func run(ctx context.Context, args []string, getenv func(string) string, logOut io.Writer) error {
	fs := flag.NewFlagSet("edge-limiter", flag.ContinueOnError)
	path := fs.String("config", getenv("EDGE_LIMITER_CONFIG"), "path to YAML config (empty = built-in defaults)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*path, getenv)
	if err != nil {
		return err
	}
	var level slog.Level
	if err := level.UnmarshalText([]byte(cfg.LogLevel)); err != nil {
		return fmt.Errorf("log_level: %w", err)
	}
	log := slog.New(slog.NewJSONHandler(logOut, &slog.HandlerOptions{Level: level}))

	m := metrics.New()
	backend, closer := server.BuildBackend(cfg, m, log)
	defer closer.Close()
	srv := server.New(cfg, backend, m, log)
	httpSrv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.Listen, "backend", cfg.Backend.Type,
			"failure_mode", cfg.Backend.FailureMode, "rules", len(cfg.Rules))
		errCh <- httpSrv.ListenAndServe()
	}()
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	log.Info("shutting down", "timeout", cfg.ShutdownTimeout)
	srv.SetReady(false)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	if err := <-errCh; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	log.Info("stopped")
	return nil
}
