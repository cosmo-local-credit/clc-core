package store

import (
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestUsernameStemsUseTheFullName(t *testing.T) {
	for _, name := range []string{"Sohail Khan", "Željko", "Софија", "李明", "سُهيل", "A", "123", "alice.eth", "A / B", "👩🏽‍🌾"} {
		require.Equal(t, name, usernameStem("  "+name+"  "))
		require.True(t, ValidUsername(name))
	}
	require.Equal(t, "José", usernameStem("Jose\u0301"))
	require.Equal(t, strings.Repeat("界", 80), usernameStem(strings.Repeat("界", 81)))
	for _, name := range []string{"", " \t\n", strings.Repeat("界", 81)} {
		require.False(t, ValidUsername(name))
	}
}
