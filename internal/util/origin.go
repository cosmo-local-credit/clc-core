package util

import (
	"strings"
)

// OriginAllowed reports whether origin matches any allowed origin pattern.
// Patterns may be exact origins or contain a single '*' that matches exactly
// one non-empty DNS label.
func OriginAllowed(origin string, allowed []string) bool {
	for _, a := range allowed {
		if a == origin {
			return true
		}
		if WildcardOriginMatch(a, origin) {
			return true
		}
	}
	return false
}

// WildcardOriginMatch reports whether pattern (containing a single '*')
// matches origin. The '*' matches one non-empty label without dots, so a
// suffix anchor like ".clc-app.pages.dev" cannot be bypassed by sibling
// domains.
func WildcardOriginMatch(pattern, origin string) bool {
	prefix, suffix, found := strings.Cut(pattern, "*")
	if !found {
		return false
	}
	if !strings.HasPrefix(origin, prefix) || !strings.HasSuffix(origin, suffix) {
		return false
	}
	label := origin[len(prefix) : len(origin)-len(suffix)]
	return label != "" && !strings.Contains(label, ".")
}
