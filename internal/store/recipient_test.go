package store

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUsernameStemsUseTheFullName(t *testing.T) {
	for _, hint := range []string{"Sohail Khan", "  Sohail   Khan  ", "Sohail\tKhan"} {
		t.Run(hint, func(t *testing.T) {
			require.Equal(t, "sohail-khan", usernameStem(hint))
		})
	}
	for _, hint := range []string{"Ab", "A", "سُهيل", "Софија", "李明", "", "123"} {
		stem := usernameStem(hint)
		require.False(t, ValidUsername(stem), hint)
		require.NotContains(t, stem, "user", "must not replace the name with a generic identity")
	}
	require.Equal(t, "sohail", usernameStem("Sohail"))
	require.Equal(t, "new-pool", usernameStem("New Pool"))
	require.True(t, ValidUsername(usernameStem(strings.Repeat("a", 100))+"99"))
}
