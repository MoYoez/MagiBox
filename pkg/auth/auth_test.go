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
	t.Cleanup(func() { _ = internal.SetRole(chatID, internal.RoleUser) })

	if got := public.RoleOf(chatID); got != public.RoleAdmin {
		t.Fatalf("RoleOf(%d) = %v, want admin", chatID, got)
	}
	if !public.Has(chatID, public.RoleAdmin) {
		t.Fatal("Has should observe the internal admin role")
	}
	if public.RequireAdmin() == nil || public.RequireOwner() == nil {
		t.Fatal("authorization middleware constructors must return middleware")
	}
}
