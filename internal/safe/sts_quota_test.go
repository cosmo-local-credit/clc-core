package safe

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSTSSharedMonthlyQuotaResumesAtProviderReset(t *testing.T) {
	const secret = "test-secret"
	var calls atomic.Int32
	var logs bytes.Buffer
	logg := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	quota := NewSTSQuota(logg)
	now := time.Date(2026, 11, 24, 16, 0, 0, 0, time.UTC)
	quota.now = func() time.Time { return now }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set(quotaRemainingHeader, "0")
			w.Header().Set(quotaResetHeader, "120")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error_msg":"provider-specific `+secret+`"}`)
			return
		}
		w.Header().Set(quotaRemainingHeader, "5000")
		w.Header().Set(quotaWarningHeader, "90% uses "+secret)
		_, _ = io.WriteString(w, `{"results":[]}`)
	}))
	t.Cleanup(srv.Close)
	first := NewSTS(srv.URL, secret).WithQuota(quota)
	other := NewSTS(srv.URL, secret).WithQuota(quota)

	_, err := first.Queued(t.Context(), stsSafe, "14")
	var upstreamErr *STSError
	require.ErrorAs(t, err, &upstreamErr)
	require.Equal(t, http.StatusTooManyRequests, upstreamErr.Status)
	require.NotContains(t, err.Error(), secret)
	wantReset := now.Add(2 * time.Minute)
	limit, limited := QuotaFailure(fmt.Errorf("wrap: %w", err))
	require.True(t, limited)
	require.Equal(t, QuotaLimit{Monthly: true, ResetAt: wantReset, RetryAt: wantReset}, limit)
	require.EqualValues(t, 1, calls.Load())

	now = wantReset.Add(-time.Millisecond)
	err = other.Confirm(t.Context(), stsHash, "0x11")
	stored, limited := QuotaFailure(err)
	require.True(t, limited)
	require.Equal(t, limit, stored)
	require.EqualValues(t, 1, calls.Load(), "the cooldown should block this write locally")

	now = wantReset
	for range 2 {
		list, err := other.Queued(t.Context(), stsSafe, "14")
		require.NoError(t, err)
		require.Empty(t, list)
	}
	require.EqualValues(t, 3, calls.Load())
	output := logs.String()
	require.Contains(t, output, `"operation":"queue"`)
	require.Contains(t, output, `"upstream_status":429,"quota_remaining":0`)
	require.Contains(t, output, `"quota_remaining":5000`)
	require.Equal(t, 1, strings.Count(output, "Safe API quota is running low"))
	require.NotContains(t, output, secret)
	require.NotContains(t, output, stsSafe)
}

func TestSTSMonthlyQuotaWithoutResetUsesOneHourCooldown(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set(quotaResetHeader, "not a time")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error_msg":"The monthly quota of 50000 requests is used."}`)
			return
		}
		_, _ = io.WriteString(w, `{"results":[]}`)
	}))
	t.Cleanup(srv.Close)
	quota := NewSTSQuota(nil)
	now := time.Date(2026, 11, 24, 16, 0, 0, 0, time.UTC)
	quota.now = func() time.Time { return now }
	c := NewSTS(srv.URL, "test-key").WithQuota(quota)

	_, err := c.Queued(t.Context(), stsSafe, "14")
	limit, limited := QuotaFailure(err)
	require.True(t, limited)
	require.Equal(t, QuotaLimit{Monthly: true, RetryAt: now.Add(time.Hour)}, limit)
	now = limit.RetryAt.Add(-time.Millisecond)
	_, err = c.Queued(t.Context(), stsSafe, "14")
	require.Error(t, err)
	require.EqualValues(t, 1, calls.Load())
	now = limit.RetryAt
	_, err = c.Queued(t.Context(), stsSafe, "14")
	require.NoError(t, err)
	require.EqualValues(t, 2, calls.Load())
}

func TestSTSShortThrottlePreservesRetryAfterWithoutMonthlyCooldown(t *testing.T) {
	now := time.Date(2026, 11, 24, 16, 0, 0, 0, time.UTC)
	tests := []struct {
		name  string
		delay string
		want  time.Duration
	}{
		{"seconds", "2", 2 * time.Second},
		{"date", now.Add(3 * time.Second).Format(http.TimeFormat), 3 * time.Second},
		{"absent", "", time.Second},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if calls.Add(1) == 1 {
					w.Header().Set(quotaRemainingHeader, "45000")
					w.Header().Set(quotaResetHeader, "2000000")
					w.Header().Set("Retry-After", tc.delay)
					w.WriteHeader(http.StatusTooManyRequests)
					_, _ = io.WriteString(w, `{"error_msg":"Too many requests per second"}`)
					return
				}
				_, _ = io.WriteString(w, `{"results":[]}`)
			}))
			t.Cleanup(srv.Close)
			quota := NewSTSQuota(nil)
			quota.now = func() time.Time { return now }
			c := NewSTS(srv.URL, "").WithQuota(quota)

			err := c.Confirm(t.Context(), stsHash, "0x11")
			limit, limited := QuotaFailure(err)
			require.True(t, limited)
			require.Equal(t, QuotaLimit{RetryAt: now.Add(tc.want)}, limit)
			require.EqualValues(t, 1, calls.Load(), "writes must not retry on upstream throttle")
			_, err = c.Queued(t.Context(), stsSafe, "14")
			require.NoError(t, err)
			require.EqualValues(t, 2, calls.Load(), "short throttling must not halt all operations")
		})
	}
}
