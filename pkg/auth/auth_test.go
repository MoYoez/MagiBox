package auth_test

import (
	"path/filepath"
	"testing"

	internal "github.com/moyoez/magibox/internal/auth"
	public "github.com/moyoez/magibox/pkg/auth"
)

func TestFacadeUsesInternalAuthorizationState(t *testing.T) {
	if err := internal.Init(filepath.Join(t.TempDir(), "auth.json")); err != nil {
		t.Fatal(err)
	}
	const chatID int64 = 42001
	if err := internal.SetRole(chatID, internal.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	if err := internal.GrantPermissions(chatID, "auth:aff", "auth:report"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := internal.RevokePermissions(chatID, "auth:aff", "auth:report"); err != nil {
			t.Errorf("reset permissions: %v", err)
		}
		if err := internal.SetRole(chatID, internal.RoleUser); err != nil {
			t.Errorf("reset role: %v", err)
		}
	})

	if got := public.RoleOf(chatID); got != public.RoleAdmin {
		t.Fatalf("RoleOf(%d) = %v, want admin", chatID, got)
	}
	if !public.Has(chatID, public.RoleAdmin) {
		t.Fatal("Has should observe the internal admin role")
	}
	permission, ok := public.ParsePermission("AUTH:AFF")
	if !ok || permission != "auth:aff" || !public.HasPermission(chatID, permission) {
		t.Fatal("permission facade should validate and observe internal authorization state")
	}
	if got := public.Permissions(chatID); len(got) != 2 {
		t.Fatalf("Permissions(%d) = %v", chatID, got)
	}
	if public.RequireAdmin() == nil || public.RequireOwner() == nil || public.RequirePermission(permission) == nil {
		t.Fatal("authorization middleware constructors must return middleware")
	}
}
