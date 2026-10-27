package store

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseUserRolesCanonicalizesAndDeduplicates(t *testing.T) {
	roles, err := ParseUserRoles([]string{"admin,SUPERADMIN", " ADMIN "})
	require.NoError(t, err)
	assert.Equal(t, []UserRole{UserRoleAdmin, UserRoleSuperadmin}, roles)
}

func TestParseUserRolesRejectsUnknownValues(t *testing.T) {
	_, err := ParseUserRoles([]string{"ADMIN,OWNER"})
	assert.ErrorIs(t, err, ErrUnknownUserRole)
}

func TestAdministrativeRoles(t *testing.T) {
	assert.True(t, IsAdministrativeRole("ADMIN"))
	assert.True(t, IsAdministrativeRole("SUPERADMIN"))
	assert.False(t, IsAdministrativeRole("USER"))
	assert.True(t, IsSuperadminRole("SUPERADMIN"))
	assert.False(t, IsSuperadminRole("ADMIN"))
}
