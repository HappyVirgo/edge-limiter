-- Sliding-window counter. Mirrors slidingCounter() in algorithms.go.
-- KEYS[1]: state hash {win, curr, prev}. ARGV: now_us, cost, limit, period_us.
local key = KEYS[1]
local now = tonumber(ARGV[1])
local cost = tonumber(ARGV[2])
local limit = tonumber(ARGV[3])
local period = tonumber(ARGV[4])

local st = redis.call('HMGET', key, 'win', 'curr', 'prev')
local swin = tonumber(st[1]) or 0
local curr = tonumber(st[2]) or 0
local prev = tonumber(st[3]) or 0

local win = math.floor(now / period)
if win ~= swin then
  if win == swin + 1 then prev = curr else prev = 0 end
  curr = 0
end

local function reset()
  if curr > 0 then return (win + 2) * period end
  if prev > 0 then return (win + 1) * period end
  return now
end

local elapsed = now - win * period
local weight = (period - elapsed) / period
local est = prev * weight + curr

if est + cost <= limit then
  curr = curr + cost
  redis.call('HSET', key, 'win', string.format('%.0f', win), 'curr', string.format('%.0f', curr),
    'prev', string.format('%.0f', prev))
  redis.call('PEXPIRE', key, math.ceil(2 * period / 1000))
  return {1, math.floor(limit - est - cost), reset(), 0}
end

local retry
if curr + cost <= limit then
  retry = period - elapsed - (limit - curr - cost) * period / prev
else
  retry = (period - elapsed) + period - (limit - cost) * period / curr
end
return {0, math.max(math.floor(limit - est), 0), reset(), math.max(math.ceil(retry), 1)}
