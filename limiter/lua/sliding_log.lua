-- Sliding-window log. Mirrors slidingLog() in algorithms.go.
-- One sorted-set member per admitted unit, scored by its admission time.
-- KEYS[1]: state key. ARGV: now_us, cost, limit, period_us, nonce.
local key = KEYS[1]
local now = tonumber(ARGV[1])
local cost = tonumber(ARGV[2])
local limit = tonumber(ARGV[3])
local period = tonumber(ARGV[4])

redis.call('ZREMRANGEBYSCORE', key, '-inf', string.format('%.0f', now - period))
local total = redis.call('ZCARD', key)

if total + cost <= limit then
  local ts = string.format('%.0f', now)
  for i = 1, cost do
    redis.call('ZADD', key, ts, ts .. ':' .. ARGV[5] .. ':' .. i)
  end
  redis.call('PEXPIRE', key, math.ceil(period / 1000))
  return {1, limit - total - cost, now + period, 0}
end

local need = total + cost - limit
local oldest = redis.call('ZRANGE', key, need - 1, need - 1, 'WITHSCORES')
local newest = redis.call('ZRANGE', key, -1, -1, 'WITHSCORES')
return {0, limit - total, tonumber(newest[2]) + period, tonumber(oldest[2]) + period - now}
