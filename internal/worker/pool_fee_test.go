package worker

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFeePercentToPPM(t *testing.T) {
	for _, tc := range []struct {
		percent float64
		want    uint64
	}{
		{0, 0},
		{0.5, 5_000},
		{1, 10_000},
		{2, 20_000},
		{100, 1_000_000},
		// Truncation cases: float64 holds each of these just under the
		// decimal value, so a plain uint64 conversion lands a PPM low.
		{0.57, 5_700},
		{0.69, 6_900},
		{1.13, 11_300},
		{2.01, 20_100},
	} {
		require.Equal(t, tc.want, feePercentToPPM(tc.percent), "%v%%", tc.percent)
	}
}

// Every percentage the API accepts, at the precision a client is likely to
// send, must survive the conversion to the PPM a human would have written.
func TestFeePercentToPPMRoundTrip(t *testing.T) {
	for i := 0; i <= 10_000; i++ {
		percent, err := strconv.ParseFloat(strconv.FormatFloat(float64(i)/100, 'f', -1, 64), 64)
		require.NoError(t, err)
		require.Equal(t, uint64(i)*100, feePercentToPPM(percent), "%v%%", percent)
	}
}
