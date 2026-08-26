-- Sliding-window reservation.
-- KEYS[1]: window key
-- ARGV[1]: max entries in the window
-- ARGV[2]: window length in milliseconds
-- ARGV[3]: unique member for this request (released later on failure)
-- Returns {1, 0} when reserved, {0, retry_after_ms} when full.

local key = KEYS[1]
local max = tonumber(ARGV[1])
local window = tonumber(ARGV[2])
local member = ARGV[3]

local now = redis.call('TIME')
local now_ms = tonumber(now[1]) * 1000 + math.floor(tonumber(now[2]) / 1000)

redis.call('ZREMRANGEBYSCORE', key, '-inf', now_ms - window)

if redis.call('ZCARD', key) >= max then
    -- Oldest entry decides when a slot frees up.
    local oldest = redis.call('ZRANGE', key, 0, 0, 'WITHSCORES')
    local retry = window
    if oldest[2] then
        retry = tonumber(oldest[2]) + window - now_ms
        if retry < 1 then retry = 1 end
    end
    return {0, retry}
end

redis.call('ZADD', key, now_ms, member)
redis.call('PEXPIRE', key, window)
return {1, 0}
