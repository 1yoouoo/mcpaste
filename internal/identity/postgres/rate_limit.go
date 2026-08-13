package postgres

import (
	"context"
	"time"

	"github.com/1yoouoo/mcpaste/internal/identity"
)

func (s *Store) ConsumeRateLimit(ctx context.Context, rule identity.RateRule, subjectHash []byte, now time.Time) (identity.RateDecision, error) {
	resetBefore := now.Add(-rule.Window)
	rowExpires := now.Add(rule.Window + identity.RateLimitRetention)
	var count int
	var started time.Time
	err := s.pool.QueryRow(ctx, `
insert into rate_limit_buckets(scope, subject_hash, window_started_at, request_count, expires_at)
values ($1, $2, $3, 1, $4)
on conflict (scope, subject_hash) do update set
    window_started_at = case
        when rate_limit_buckets.window_started_at <= $5 then excluded.window_started_at
        else rate_limit_buckets.window_started_at
    end,
    request_count = case
        when rate_limit_buckets.window_started_at <= $5 then 1
        else rate_limit_buckets.request_count + 1
    end,
    expires_at = case
        when rate_limit_buckets.window_started_at <= $5 then excluded.expires_at
        else rate_limit_buckets.expires_at
    end
returning request_count, window_started_at`,
		rule.Scope, subjectHash, now, rowExpires, resetBefore,
	).Scan(&count, &started)
	if err != nil {
		return identity.RateDecision{}, err
	}
	if count <= rule.Limit {
		return identity.RateDecision{Allowed: true}, nil
	}
	retry := started.Add(rule.Window).Sub(now)
	if retry < time.Second {
		retry = time.Second
	}
	return identity.RateDecision{Allowed: false, RetryAfter: retry}, nil
}
