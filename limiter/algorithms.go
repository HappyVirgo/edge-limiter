package limiter

import "math"

// The functions in this file are the reference implementations of each
// algorithm. limiter/lua/*.lua mirror them operation-for-operation; the
// equivalence test in redis_test.go enforces that both agree exactly.

// gcraState holds the theoretical arrival time (TAT) in unix microseconds.
type gcraState struct{ tat int64 }

// gcra implements a token bucket with the generic cell rate algorithm: each
// unit of cost pushes the TAT forward by one emission interval and a request
// is admitted while the TAT stays within burst intervals of now.
func gcra(st *gcraState, now, cost int64, l Limit) result {
	interval := float64(l.periodMicros()) / float64(l.Rate)
	burstOffset := interval * float64(l.burst())
	tat := math.Max(float64(st.tat), float64(now))
	newTat := tat + interval*float64(cost)
	allowAt := newTat - burstOffset
	remaining := math.Floor((float64(now) - allowAt) / interval)
	if remaining < 0 {
		avail := math.Floor((float64(now) - (tat - burstOffset)) / interval)
		return result{
			remaining: int64(math.Max(avail, 0)),
			resetUS:   int64(math.Ceil(tat)),
			retryUS:   int64(math.Ceil(allowAt - float64(now))),
		}
	}
	st.tat = int64(math.Ceil(newTat))
	return result{allowed: true, remaining: int64(remaining), resetUS: st.tat}
}

// logEntry records n units admitted at ts (unix microseconds).
type logEntry struct{ ts, n int64 }

// logState is the ordered list of admitted units still inside the window.
type logState struct {
	entries []logEntry
	total   int64
}

// slidingLog admits cost units if fewer than Rate units were admitted in the
// trailing Period. An entry at ts expires once ts <= now-Period.
func slidingLog(st *logState, now, cost int64, l Limit) result {
	period := l.periodMicros()
	cutoff := now - period
	i := 0
	for i < len(st.entries) && st.entries[i].ts <= cutoff {
		st.total -= st.entries[i].n
		i++
	}
	if i > 0 {
		st.entries = append(st.entries[:0], st.entries[i:]...)
	}
	if st.total+cost <= l.Rate {
		if n := len(st.entries); n > 0 && st.entries[n-1].ts == now {
			st.entries[n-1].n += cost
		} else {
			st.entries = append(st.entries, logEntry{ts: now, n: cost})
		}
		st.total += cost
		return result{allowed: true, remaining: l.Rate - st.total, resetUS: now + period}
	}
	// Denied: wait until enough of the oldest units expire to fit cost.
	need := st.total + cost - l.Rate
	var freed, retry int64
	for _, e := range st.entries {
		freed += e.n
		if freed >= need {
			retry = e.ts + period - now
			break
		}
	}
	newest := st.entries[len(st.entries)-1].ts
	return result{remaining: l.Rate - st.total, resetUS: newest + period, retryUS: retry}
}

// counterState holds the counts of the current and previous fixed windows.
type counterState struct{ win, curr, prev int64 }

// slidingCounter estimates the trailing-window count as
// prev*(remaining fraction of the current window) + curr.
func slidingCounter(st *counterState, now, cost int64, l Limit) result {
	p := float64(l.periodMicros())
	win := int64(math.Floor(float64(now) / p))
	if win != st.win {
		if win == st.win+1 {
			st.prev = st.curr
		} else {
			st.prev = 0
		}
		st.curr = 0
		st.win = win
	}
	elapsed := float64(now) - float64(win)*p
	weight := (p - elapsed) / p
	est := float64(st.prev)*weight + float64(st.curr)
	limit := float64(l.Rate)
	if est+float64(cost) <= limit {
		st.curr += cost
		return result{
			allowed:   true,
			remaining: int64(math.Floor(limit - est - float64(cost))),
			resetUS:   counterReset(st, now, p),
		}
	}
	var retry float64
	if st.curr+cost <= l.Rate {
		// The previous window's weight must decay further within this window.
		retry = p - elapsed - (limit-float64(st.curr)-float64(cost))*p/float64(st.prev)
	} else {
		// Wait for the next window, where today's count becomes "prev".
		retry = (p - elapsed) + p - (limit-float64(cost))*p/float64(st.curr)
	}
	return result{
		remaining: int64(math.Max(math.Floor(limit-est), 0)),
		resetUS:   counterReset(st, now, p),
		retryUS:   int64(math.Max(math.Ceil(retry), 1)),
	}
}

// counterReset returns when the estimate decays to zero.
func counterReset(st *counterState, now int64, p float64) int64 {
	switch {
	case st.curr > 0:
		return int64(float64(st.win+2) * p)
	case st.prev > 0:
		return int64(float64(st.win+1) * p)
	default:
		return now
	}
}
