-- Release a reservation taken by sliding_window_reserve.
-- KEYS[1]: window key
-- ARGV[1]: the member returned by the reserve call
-- Idempotent: removing an already-expired member is a no-op.

return redis.call('ZREM', KEYS[1], ARGV[1])
