package perm

import (
	"slices"
	"testing"

	"github.com/moyoez/magibox/internal/auth"
)

func TestParsePermissionsSupportsStackedScopes(t *testing.T) {
	permissions, err := parsePermissions([]string{"AUTH:AFF", "auth:report"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(permissions, []auth.Permission{"auth:aff", "auth:report"}) {
		t.Fatalf("permissions = %v", permissions)
	}
}

func TestParsePermissionsRejectsInvalidScope(t *testing.T) {
	for _, fields := range [][]string{nil, {"aff"}, {"auth:aff", "invalid permission"}} {
		if _, err := parsePermissions(fields); err == nil {
			t.Fatalf("parsePermissions(%v) should fail", fields)
		}
	}
}
