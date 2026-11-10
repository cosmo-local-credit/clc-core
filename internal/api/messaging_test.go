package api

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestMessageLimiterAllowsThirtyAMinute(t *testing.T) {
	clock := newFakeClock()
	limiter := newMarketRateLimiter(messageRateLimit, messageRateBurst, clock.Now)
	const caller = "user:7"

	for range 30 {
		allowed, _ := limiter.allow(caller)
		require.True(t, allowed, "a send inside the 30-message burst was limited")
	}
	allowed, retry := limiter.allow(caller)
	require.False(t, allowed)
	require.Equal(t, 2*time.Second, retry)

	// Sustained: one more every two seconds, so 30 over the next minute.
	sent := 0
	for range 60 {
		clock.Advance(time.Second)
		if ok, _ := limiter.allow(caller); ok {
			sent++
		}
	}
	require.Equal(t, 30, sent)

	other, _ := limiter.allow("user:8")
	require.True(t, other, "each user has their own bucket")
}

func TestMessagingCursorRoundTrips(t *testing.T) {
	at := time.Date(2026, 11, 9, 10, 0, 0, 123456000, time.UTC)
	cursor := encodeMessagingCursor(at, 42)
	keyset, err := decodeMessagingCursor(cursor)
	require.NoError(t, err)
	require.True(t, keyset.At.Equal(at))
	require.Equal(t, int64(42), keyset.ID)

	none, err := decodeMessagingCursor("")
	require.NoError(t, err)
	require.Nil(t, none)
	for _, bad := range []string{"!!", "bm90LWEtY3Vyc29y", "MTIzLjA", "MTIz"} {
		_, err := decodeMessagingCursor(bad)
		require.Error(t, err, bad)
	}
}

func TestUserMessageSenderIsRelativeToTheCaller(t *testing.T) {
	require.Equal(t, "self", userMessageSender("direct", 1, 1))
	require.Equal(t, "peer", userMessageSender("direct", 2, 1))
	require.Equal(t, "self", userMessageSender("helpdesk", 1, 1))
	require.Equal(t, "support", userMessageSender("helpdesk", 9, 1))
}
