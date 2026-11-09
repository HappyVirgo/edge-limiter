package limiter

import (
	"context"
	"hash/maphash"
	"sync"
	"time"
)

// keyState is the per-key state for whichever algorithm last used the key.
type keyState struct {
	alg       Algorithm
	gcra      gcraState
	log       logState
	counter   counterState
	expiresUS int64 // after this instant the state is equivalent to a fresh key
}

type shard struct {
	mu   sync.Mutex
	keys map[string]*keyState
}

// Memory is an in-process Backend. Keys are spread over independently locked
// shards so that unrelated keys rarely contend on the same mutex.
type Memory struct {
	clock  Clock
	seed   maphash.Seed
	shards []shard
	mask   uint64
	stop   chan struct{}
	once   sync.Once
}

// MemoryOption configures a Memory backend.
type MemoryOption func(*memoryConfig)

type memoryConfig struct {
	clock         Clock
	shards        int
	sweepInterval time.Duration
}

// WithClock overrides the time source (default SystemClock).
func WithClock(c Clock) MemoryOption { return func(m *memoryConfig) { m.clock = c } }

// WithShards sets the shard count, rounded up to a power of two (default 64).
func WithShards(n int) MemoryOption { return func(m *memoryConfig) { m.shards = n } }

// WithSweepInterval enables a background goroutine that evicts expired keys
// every d. Zero (the default) disables it; call Sweep manually instead.
func WithSweepInterval(d time.Duration) MemoryOption {
	return func(m *memoryConfig) { m.sweepInterval = d }
}

// NewMemory creates an in-memory backend.
func NewMemory(opts ...MemoryOption) *Memory {
	cfg := memoryConfig{clock: SystemClock{}, shards: 64}
	for _, o := range opts {
		o(&cfg)
	}
	n := 1
	for n < cfg.shards {
		n <<= 1
	}
	m := &Memory{
		clock:  cfg.clock,
		seed:   maphash.MakeSeed(),
		shards: make([]shard, n),
		mask:   uint64(n - 1),
		stop:   make(chan struct{}),
	}
	for i := range m.shards {
		m.shards[i].keys = make(map[string]*keyState)
	}
	if cfg.sweepInterval > 0 {
		go m.sweepLoop(cfg.sweepInterval)
	}
	return m
}

// Name implements Backend.
func (m *Memory) Name() string { return "memory" }

// Allow implements Backend.
func (m *Memory) Allow(_ context.Context, key string, cost int64, limit Limit) (Decision, error) {
	if err := checkArgs(cost, limit); err != nil {
		return Decision{}, err
	}
	now := m.clock.Now().UnixMicro()
	sh := &m.shards[maphash.String(m.seed, key)&m.mask]

	sh.mu.Lock()
	st, ok := sh.keys[key]
	if !ok || st.alg != limit.Algorithm {
		st = &keyState{alg: limit.Algorithm}
		sh.keys[key] = st
	}
	var r result
	switch limit.Algorithm {
	case TokenBucket:
		r = gcra(&st.gcra, now, cost, limit)
	case SlidingWindowLog:
		r = slidingLog(&st.log, now, cost, limit)
	case SlidingWindowCounter:
		r = slidingCounter(&st.counter, now, cost, limit)
	}
	st.expiresUS = max(st.expiresUS, r.resetUS)
	sh.mu.Unlock()

	return r.decision(limit), nil
}

// Sweep removes keys whose state has fully expired and returns how many.
func (m *Memory) Sweep() int {
	now := m.clock.Now().UnixMicro()
	removed := 0
	for i := range m.shards {
		sh := &m.shards[i]
		sh.mu.Lock()
		for k, st := range sh.keys {
			if st.expiresUS <= now {
				delete(sh.keys, k)
				removed++
			}
		}
		sh.mu.Unlock()
	}
	return removed
}

// Len returns the number of tracked keys.
func (m *Memory) Len() int {
	n := 0
	for i := range m.shards {
		m.shards[i].mu.Lock()
		n += len(m.shards[i].keys)
		m.shards[i].mu.Unlock()
	}
	return n
}

// Close stops the background sweeper, if any.
func (m *Memory) Close() error {
	m.once.Do(func() { close(m.stop) })
	return nil
}

func (m *Memory) sweepLoop(d time.Duration) {
	t := time.NewTicker(d)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			m.Sweep()
		case <-m.stop:
			return
		}
	}
}
