-- Token bucket (GCRA). Mirrors gcra() in algorithms.go.
-- KEYS[1]: state key. ARGV: now_us, cost, rate, period_us, burst.
local now = tonumber(ARGV[1])
local cost = tonumber(ARGV[2])
local rate = tonumber(ARGV[3])
local period = tonumber(ARGV[4])
local burst = tonumber(ARGV[5])

local interval = period / rate
local burst_offset = interval * burst
local tat = math.max(tonumber(redis.call('GET', KEYS[1])) or 0, now)
local new_tat = tat + interval * cost
local allow_at = new_tat - burst_offset
local remaining = math.floor((now - allow_at) / interval)

if remaining < 0 then
  local avail = math.floor((now - (tat - burst_offset)) / interval)
  return {0, math.max(avail, 0), math.ceil(tat), math.ceil(allow_at - now)}
end

local stored = math.ceil(new_tat)
redis.call('SET', KEYS[1], string.format('%.0f', stored), 'PX', math.ceil((stored - now) / 1000))
return {1, remaining, stored, 0}
