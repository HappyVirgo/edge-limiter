package limiter

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"fmt"
	"strconv"
	"sync/atomic"

	"github.com/redis/go-redis/v9"
)

var (
	//go:embed lua/token_bucket.lua
	tokenBucketLua string
	//go:embed lua/sliding_log.lua
	slidingLogLua string
	//go:embed lua/sliding_counter.lua
	slidingCounterLua string

	tokenBucketScript    = redis.NewScript(tokenBucketLua)
	slidingLogScript     = redis.NewScript(slidingLogLua)
	slidingCounterScript = redis.NewScript(slidingCounterLua)
)

// keyTags keep the state of different algorithms for the same key apart.
var keyTags = map[Algorithm]string{TokenBucket: "tb:", SlidingWindowLog: "swl:", SlidingWindowCounter: "swc:"}

// Redis is a Backend that keeps state in Redis and makes each decision
// atomically inside a Lua script, so many limiter replicas can share limits.
//
// Time comes from the replica's Clock and is passed to the script, which keeps
// the scripts deterministic and testable; replicas therefore need reasonably
// synchronised clocks (NTP).
type Redis struct {
	client redis.Scripter
	pinger func(context.Context) error
	clock  Clock
	prefix string
	nonce  string
	seq    atomic.Uint64
}

// NewRedis wraps a go-redis client. prefix namespaces every key (e.g. "rl:").
func NewRedis(client redis.UniversalClient, prefix string, clock Clock) *Redis {
	if clock == nil {
		clock = SystemClock{}
	}
	var b [6]byte
	_, _ = rand.Read(b[:])
	return &Redis{
		client: client,
		pinger: func(ctx context.Context) error { return client.Ping(ctx).Err() },
		clock:  clock,
		prefix: prefix,
		nonce:  hex.EncodeToString(b[:]),
	}
}

// Name implements Backend.
func (r *Redis) Name() string { return "redis" }

// Ping implements Pinger.
func (r *Redis) Ping(ctx context.Context) error { return r.pinger(ctx) }

// Allow implements Backend.
func (r *Redis) Allow(ctx context.Context, key string, cost int64, limit Limit) (Decision, error) {
	if err := checkArgs(cost, limit); err != nil {
		return Decision{}, err
	}
	now := r.clock.Now().UnixMicro()
	keys := []string{r.prefix + keyTags[limit.Algorithm] + key}
	period := limit.periodMicros()

	var res any
	var err error
	switch limit.Algorithm {
	case TokenBucket:
		res, err = tokenBucketScript.Run(ctx, r.client, keys, now, cost, limit.Rate, period, limit.burst()).Result()
	case SlidingWindowLog:
		nonce := r.nonce + strconv.FormatUint(r.seq.Add(1), 36)
		res, err = slidingLogScript.Run(ctx, r.client, keys, now, cost, limit.Rate, period, nonce).Result()
	case SlidingWindowCounter:
		res, err = slidingCounterScript.Run(ctx, r.client, keys, now, cost, limit.Rate, period).Result()
	}
	if err != nil {
		return Decision{}, fmt.Errorf("redis %s: %w", limit.Algorithm, err)
	}
	out, err := parseScriptResult(res)
	if err != nil {
		return Decision{}, err
	}
	return out.decision(limit), nil
}

func parseScriptResult(v any) (result, error) {
	vals, ok := v.([]any)
	if !ok || len(vals) != 4 {
		return result{}, fmt.Errorf("redis: unexpected script reply %#v", v)
	}
	var n [4]int64
	for i, x := range vals {
		if n[i], ok = x.(int64); !ok {
			return result{}, fmt.Errorf("redis: unexpected script reply element %#v", x)
		}
	}
	return result{allowed: n[0] == 1, remaining: n[1], resetUS: n[2], retryUS: n[3]}, nil
}
