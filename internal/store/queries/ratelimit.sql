-- Rate limit counters (internal/ratelimit).

-- name: HitRateLimits :many
-- Counts one event in the current one-minute window (the database clock)
-- of every bucket, in sorted order so concurrent requests lock rows in the
-- same order. reset_seconds is how long until the window ends.
INSERT INTO rate_limit_counters AS c (bucket, window_start)
SELECT b.bucket, date_trunc('minute', now())
FROM unnest(@buckets::text[]) AS b(bucket)
ORDER BY b.bucket
ON CONFLICT (bucket, window_start) DO UPDATE SET count = c.count + 1
RETURNING c.bucket, c.count,
    EXTRACT(EPOCH FROM (c.window_start + interval '1 minute' - clock_timestamp()))::float8 AS reset_seconds;

-- name: DeleteOldRateLimitCounters :execrows
DELETE FROM rate_limit_counters WHERE window_start < now() - interval '2 minutes';
