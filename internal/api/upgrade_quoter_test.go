package api

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// A pool's quoter_type, not whichever platform_contracts row happens to exist,
// decides the implementation its quoter proxy may be upgraded to. Dispatching
// on the row sent every RelativeQuoter to the OracleQuoter implementation,
// whose storage disagrees from slot 0, bricking the pool's swaps.
func TestQuoterContractType(t *testing.T) {
	quoterType := func(s string) *string { return &s }

	for name, tc := range map[string]struct {
		quoterType *string
		want       string
	}{
		"oracle":            {quoterType("oracle"), "ORACLE_QUOTER"},
		"relative":          {quoterType("relative"), "RELATIVE_QUOTER"},
		"decimal is shared": {quoterType("decimal"), ""},
		"unrecorded":        {nil, ""},
		"unknown":           {quoterType("something-else"), ""},
		"empty":             {quoterType(""), ""},
	} {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, quoterContractType(tc.quoterType))
		})
	}
}

func TestSkippedOrEmptySerializesAsArray(t *testing.T) {
	require.Equal(t, []skippedUpgrade{}, skippedOrEmpty(nil))

	skipped := []skippedUpgrade{{Type: "SWAPPOOL", Address: "0x1", Reason: "already on the current implementation"}}
	require.Equal(t, skipped, skippedOrEmpty(skipped))
}
