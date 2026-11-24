package safe

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type (
	// QuotaLimit describes a Safe API limit. ResetAt is reported only for a
	// monthly limit with a known provider reset. RetryAt also covers fallbacks.
	QuotaLimit struct {
		Monthly bool
		ResetAt time.Time
		RetryAt time.Time
	}

	// QuotaLimitError carries the quota limit on a failed provider request.
	QuotaLimitError QuotaLimit

	// STSQuota shares monthly cooldown and quota warnings across local clients
	// configured for the same Safe API account.
	STSQuota struct {
		logg *slog.Logger
		now  func() time.Time

		mu          sync.Mutex
		limit       QuotaLimit
		lastWarning time.Time
	}
)

const (
	quotaResetHeader     = "X-RateLimit-Reset"
	quotaRemainingHeader = "X-RateLimit-Remaining"
	quotaWarningHeader   = "X-RateLimit-Warning"
	quotaResetFallback   = time.Hour
	maxQuotaBody         = 4 << 10
)

// NewSTSQuota creates the process-local quota observer and cooldown gate.
func NewSTSQuota(logg *slog.Logger) *STSQuota {
	return &STSQuota{logg: logg, now: time.Now}
}

func (limit *QuotaLimitError) Error() string {
	if limit.Monthly {
		return "Safe API monthly quota exhausted"
	}
	return "Safe API rate limit exceeded"
}

// QuotaFailure extracts a Safe API limit even if a caller wrapped the error.
func QuotaFailure(err error) (QuotaLimit, bool) {
	var limit *QuotaLimitError
	if !errors.As(err, &limit) || limit == nil {
		return QuotaLimit{}, false
	}
	return QuotaLimit(*limit), true
}

func stsQuotaError(limit QuotaLimit) *STSError {
	err := QuotaLimitError(limit)
	return &STSError{Status: http.StatusTooManyRequests, Message: err.Error(), quota: &err}
}

func (q *STSQuota) blocked() (QuotaLimit, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.limit, q.now().Before(q.limit.RetryAt)
}

func (q *STSQuota) observeLimit(ctx context.Context, operation string, headers http.Header, body []byte) QuotaLimit {
	now := q.now()
	if !monthlyLimit(headers, body) {
		return QuotaLimit{RetryAt: retryAfter(headers.Get("Retry-After"), now)}
	}

	reset := secondsFromNow(headers.Get(quotaResetHeader), now)
	limit := QuotaLimit{Monthly: true, ResetAt: reset, RetryAt: reset}
	if reset.IsZero() {
		limit.RetryAt = now.Add(quotaResetFallback)
	}

	q.mu.Lock()
	firstDenial := !now.Before(q.limit.RetryAt)
	// A later in-flight response may refine an unknown reset. Keep a known
	// reset if another response omits it.
	switch {
	case firstDenial, q.limit.ResetAt.IsZero() && !reset.IsZero():
		q.limit = limit
	case !q.limit.ResetAt.IsZero() && !reset.IsZero() && reset.After(q.limit.ResetAt):
		q.limit = limit
	}
	limit = q.limit
	q.mu.Unlock()

	if firstDenial && q.logg != nil {
		q.logg.WarnContext(ctx, "Safe API monthly quota exhausted",
			"operation", operation, "upstream_status", http.StatusTooManyRequests,
			"quota_remaining", 0, "retry_at", limit.RetryAt)
	}
	return limit
}

func monthlyLimit(headers http.Header, body []byte) bool {
	if strings.TrimSpace(headers.Get(quotaRemainingHeader)) == "0" {
		return true
	}
	// Safe supplies a structured description when the reset headers are missing.
	if len(body) > maxQuotaBody {
		return false
	}
	var failure struct {
		Message string `json:"error_msg"`
	}
	if json.Unmarshal(body, &failure) != nil {
		return false
	}
	msg := strings.ToLower(strings.TrimSpace(failure.Message))
	return strings.Contains(msg, "monthly quota") || msg == "too many requests in time window"
}

func retryAfter(header string, now time.Time) time.Time {
	if at := secondsFromNow(header, now); !at.IsZero() {
		return at
	}
	if at, err := http.ParseTime(header); err == nil && at.After(now) {
		return at
	}
	return now.Add(time.Second)
}

func secondsFromNow(header string, now time.Time) time.Time {
	// Int32 seconds comfortably cover a 30-day window and fit a duration.
	seconds, err := strconv.ParseInt(strings.TrimSpace(header), 10, 32)
	if err != nil || seconds <= 0 {
		return time.Time{}
	}
	return now.Add(time.Duration(seconds) * time.Second)
}

func (q *STSQuota) logResponse(ctx context.Context, operation string, response *http.Response) {
	if q.logg == nil {
		return
	}
	status := response.StatusCode
	remaining, err := strconv.Atoi(response.Header.Get(quotaRemainingHeader))
	if err != nil || remaining < 0 {
		q.logg.DebugContext(ctx, "Safe API request", "operation", operation, "upstream_status", status)
		return
	}
	q.logg.DebugContext(ctx, "Safe API request", "operation", operation, "upstream_status", status, "quota_remaining", remaining)
	if status == http.StatusTooManyRequests || response.Header.Get(quotaWarningHeader) == "" {
		return
	}
	now := q.now()
	q.mu.Lock()
	warn := !now.Before(q.lastWarning.Add(time.Hour))
	if warn {
		q.lastWarning = now
	}
	q.mu.Unlock()
	if warn {
		q.logg.WarnContext(ctx, "Safe API quota is running low",
			"operation", operation, "upstream_status", status, "quota_remaining", remaining)
	}
}

func (q *STSQuota) logTransportFailure(ctx context.Context, operation string) {
	if q.logg != nil {
		q.logg.DebugContext(ctx, "Safe API request failed", "operation", operation)
	}
}
