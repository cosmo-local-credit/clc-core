package store

import (
	"errors"
	"slices"
	"strings"
)

type (
	UserRole string
)

const (
	UserRoleUser       UserRole = "USER"
	UserRoleAdmin      UserRole = "ADMIN"
	UserRoleSuperadmin UserRole = "SUPERADMIN"
)

var (
	userRoles          = []UserRole{UserRoleUser, UserRoleAdmin, UserRoleSuperadmin}
	ErrUnknownUserRole = errors.New(`role must be "USER", "ADMIN" or "SUPERADMIN"`)
)

func ParseUserRoles(rawValues []string) ([]UserRole, error) {
	seen := make(map[UserRole]struct{})
	roles := make([]UserRole, 0, len(rawValues))
	for _, raw := range rawValues {
		for value := range strings.SplitSeq(raw, ",") {
			trimmed := strings.TrimSpace(value)
			if trimmed == "" {
				continue
			}
			candidate := UserRole(strings.ToUpper(trimmed))
			if !slices.Contains(userRoles, candidate) {
				return nil, ErrUnknownUserRole
			}
			if _, ok := seen[candidate]; ok {
				continue
			}
			seen[candidate] = struct{}{}
			roles = append(roles, candidate)
		}
	}
	return roles, nil
}

func IsAdministrativeRole(role string) bool {
	return role == string(UserRoleAdmin) || role == string(UserRoleSuperadmin)
}

func IsSuperadminRole(role string) bool {
	return role == string(UserRoleSuperadmin)
}
