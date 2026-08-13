package api

import (
	"fmt"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
)

const (
	timeRange5m     = "5m"
	timeRange15m    = "15m"
	timeRange30m    = "30m"
	timeRange1h     = "1h"
	timeRange6h     = "6h"
	timeRange1d     = "1d"
	timeRangeCustom = "custom"
)

type (
	dashboardTimeWindow struct {
		Preset        string
		Since         time.Time
		Until         time.Time
		Bucket        time.Duration
		BucketSeconds int64
	}

	rangeError string
)

func (e rangeError) Error() string { return string(e) }

func rangeErr(msg string) error { return rangeError(msg) }

func parseDashboardTimeWindow(c *echo.Context) (dashboardTimeWindow, error) {
	now := time.Now().UTC()
	rangeParam := strings.TrimSpace(c.QueryParam("range"))
	if rangeParam == "" {
		rangeParam = timeRange1d
	}

	if rangeParam == timeRangeCustom || c.QueryParam("since") != "" || c.QueryParam("until") != "" {
		sinceRaw := strings.TrimSpace(c.QueryParam("since"))
		untilRaw := strings.TrimSpace(c.QueryParam("until"))
		if sinceRaw == "" || untilRaw == "" {
			return dashboardTimeWindow{}, rangeErr("custom range requires since and until (RFC3339)")
		}
		since, err := parseRFC3339(sinceRaw)
		if err != nil {
			return dashboardTimeWindow{}, rangeErr("invalid since: use RFC3339")
		}
		until, err := parseRFC3339(untilRaw)
		if err != nil {
			return dashboardTimeWindow{}, rangeErr("invalid until: use RFC3339")
		}
		since = since.UTC()
		until = until.UTC()
		if !until.After(since) {
			return dashboardTimeWindow{}, rangeErr("until must be after since")
		}
		const maxSpan = 90 * 24 * time.Hour
		if until.Sub(since) > maxSpan {
			return dashboardTimeWindow{}, rangeErr("custom range cannot exceed 90 days")
		}
		if until.Sub(since) < time.Minute {
			return dashboardTimeWindow{}, rangeErr("custom range must be at least 1 minute")
		}
		bucket := pickSeriesBucket(until.Sub(since))
		return dashboardTimeWindow{
			Preset:        timeRangeCustom,
			Since:         since,
			Until:         until,
			Bucket:        bucket,
			BucketSeconds: int64(bucket / time.Second),
		}, nil
	}

	d, err := presetDuration(rangeParam)
	if err != nil {
		return dashboardTimeWindow{}, err
	}
	bucket := pickSeriesBucket(d)
	return dashboardTimeWindow{
		Preset:        rangeParam,
		Since:         now.Add(-d),
		Until:         now,
		Bucket:        bucket,
		BucketSeconds: int64(bucket / time.Second),
	}, nil
}

func parseRFC3339(raw string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Parse(time.RFC3339Nano, raw)
	}
	return t, nil
}

func presetDuration(rangeParam string) (time.Duration, error) {
	switch rangeParam {
	case timeRange5m:
		return 5 * time.Minute, nil
	case timeRange15m:
		return 15 * time.Minute, nil
	case timeRange30m:
		return 30 * time.Minute, nil
	case timeRange1h:
		return time.Hour, nil
	case timeRange6h:
		return 6 * time.Hour, nil
	case timeRange1d:
		return 24 * time.Hour, nil
	default:
		return 0, rangeErr(fmt.Sprintf("unknown range %q; use 5m,15m,30m,1h,6h,1d,custom", rangeParam))
	}
}

func pickSeriesBucket(span time.Duration) time.Duration {
	switch {
	case span <= 5*time.Minute:
		return 30 * time.Second
	case span <= 15*time.Minute:
		return time.Minute
	case span <= 30*time.Minute:
		return 2 * time.Minute
	case span <= time.Hour:
		return 5 * time.Minute
	case span <= 6*time.Hour:
		return 15 * time.Minute
	case span <= 24*time.Hour:
		return time.Hour
	case span <= 7*24*time.Hour:
		return 6 * time.Hour
	default:
		return 24 * time.Hour
	}
}

func formatBucketLabel(d time.Duration) string {
	switch {
	case d < time.Minute:
		secs := max(1, int(d/time.Second))
		return fmt.Sprintf("%ds", secs)
	case d < time.Hour:
		mins := max(1, int(d/time.Minute))
		return fmt.Sprintf("%dm", mins)
	case d < 24*time.Hour:
		hours := max(1, int(d/time.Hour))
		return fmt.Sprintf("%dh", hours)
	default:
		days := max(1, int(d/(24*time.Hour)))
		return fmt.Sprintf("%dd", days)
	}
}
