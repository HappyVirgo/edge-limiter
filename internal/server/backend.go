package server

import (
	"crypto/tls"
	"errors"
	"io"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/HappyVirgo/edge-limiter/internal/config"
	"github.com/HappyVirgo/edge-limiter/internal/metrics"
	"github.com/HappyVirgo/edge-limiter/limiter"
)

// BuildBackend constructs the configured backend. For Redis it returns a
// Failover that applies the configured failure mode, with a local in-memory
// limiter as the fallback. The returned Closer releases connections.
func BuildBackend(cfg *config.Config, m *metrics.Metrics, log *slog.Logger) (limiter.Backend, io.Closer) {
	local := limiter.NewMemory(limiter.WithSweepInterval(30 * time.Second))
	if cfg.Backend.Type == "memory" {
		return m.Instrument(local), local
	}

	rc := cfg.Backend.Redis
	opts := &redis.Options{
		Addr:         rc.Addr,
		Password:     rc.Password,
		DB:           rc.DB,
		DialTimeout:  rc.Timeout * 4,
		ReadTimeout:  rc.Timeout,
		WriteTimeout: rc.Timeout,
		MaxRetries:   1,
	}
	if rc.TLS {
		opts.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	client := redis.NewClient(opts)
	f := &limiter.Failover{
		Primary:  m.Instrument(limiter.NewRedis(client, rc.KeyPrefix, nil)),
		Local:    m.Instrument(local),
		Mode:     cfg.Backend.FailureMode,
		Cooldown: cfg.Backend.Cooldown,
		OnFallback: func(mode limiter.FailureMode, err error) {
			m.Fallbacks.WithLabelValues(string(mode)).Inc()
			if !errors.Is(err, limiter.ErrCircuitOpen) {
				log.Warn("primary backend failed; using fallback", "mode", mode, "cooldown", cfg.Backend.Cooldown, "err", err)
			}
		},
	}
	return f, closers{client, local}
}

type closers []io.Closer

func (c closers) Close() error {
	var errs []error
	for _, x := range c {
		errs = append(errs, x.Close())
	}
	return errors.Join(errs...)
}
