package auth

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestRoleFlow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := Init(path); err != nil {
		t.Fatal(err)
	}

	// Init should generate a one-time pairing code when there is no owner.
	code := def.code
	if code == "" {
		t.Fatal("期望生成配对码")
	}

	// The pairing code binds the first owner.
	const owner int64 = 1001
	if !Bind(code, owner) {
		t.Fatal("配对码应绑定成功")
	}
	if RoleOf(owner) != RoleOwner {
		t.Fatalf("owner 角色 = %s,期望 owner", RoleOf(owner))
	}
	if Bind(code, 9999) { // one-time use
		t.Fatal("配对码应已失效")
	}

	// Role-level checks.
	const u int64 = 2002
	if Has(u, RoleAdmin) {
		t.Fatal("默认用户不应有 admin 权限")
	}
	if err := SetRole(u, RoleAdmin); err != nil {
		t.Fatal(err)
	}
	if !Has(u, RoleAdmin) {
		t.Fatal("提升后应有 admin 权限")
	}
	if Has(u, RoleOwner) {
		t.Fatal("admin 不应有 owner 权限")
	}
	if !Has(owner, RoleAdmin) {
		t.Fatal("owner 应满足 admin 要求")
	}

	// IDs(RoleAdmin) should include the owner and the newly promoted admin.
	if got := len(IDs(RoleAdmin)); got != 2 {
		t.Fatalf("admin 及以上 = %d,期望 2", got)
	}

	// Demote back to user.
	if err := SetRole(u, RoleUser); err != nil {
		t.Fatal(err)
	}
	if Has(u, RoleAdmin) {
		t.Fatal("降级后不应有 admin 权限")
	}

	// Roles should have been persisted.
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("应已持久化: %v", err)
	}
}

func TestComposablePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := Init(path); err != nil {
		t.Fatal(err)
	}

	const chatID int64 = 3003
	if err := GrantPermissions(chatID, "AUTH:AFF", "auth:report", "auth:aff"); err != nil {
		t.Fatal(err)
	}
	if got := RoleOf(chatID); got != RoleUser {
		t.Fatalf("role = %s, want user", got)
	}
	if !HasPermission(chatID, "auth:aff") || !HasPermission(chatID, "auth:report") {
		t.Fatal("ordinary user should receive stacked explicit permissions")
	}
	if got := Permissions(chatID); !slices.Equal(got, []Permission{"auth:aff", "auth:report"}) {
		t.Fatalf("permissions = %v", got)
	}

	if err := SetRole(chatID, RoleAdmin); err != nil {
		t.Fatal(err)
	}
	if !HasPermission(chatID, "auth:anything") {
		t.Fatal("admin should inherit business permissions")
	}
	if err := SetRole(chatID, RoleUser); err != nil {
		t.Fatal(err)
	}
	if !HasPermission(chatID, "auth:aff") {
		t.Fatal("demoting a role must preserve explicit permissions")
	}

	if err := RevokePermissions(chatID, "auth:aff"); err != nil {
		t.Fatal(err)
	}
	if HasPermission(chatID, "auth:aff") || !HasPermission(chatID, "auth:report") {
		t.Fatal("revoking one permission must preserve other grants")
	}
	if err := Init(path); err != nil {
		t.Fatal(err)
	}
	if got := Permissions(chatID); !slices.Equal(got, []Permission{"auth:report"}) {
		t.Fatalf("reloaded permissions = %v", got)
	}
	members := Members()
	if len(members) != 1 || members[0].ID != chatID || members[0].Role != RoleUser {
		t.Fatalf("members = %#v", members)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("auth file mode = %o, want 600", got)
	}
}

func TestLegacyRoleStoreRemainsCompatible(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	legacy := []byte(`{"members":[{"id":1001,"role":2},{"id":2002,"role":1}]}`)
	if err := os.WriteFile(path, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Init(path); err != nil {
		t.Fatal(err)
	}
	if RoleOf(1001) != RoleOwner || RoleOf(2002) != RoleAdmin {
		t.Fatalf("legacy roles were not restored: owner=%s admin=%s", RoleOf(1001), RoleOf(2002))
	}
	if !HasPermission(2002, "auth:aff") {
		t.Fatal("legacy admin should inherit business permissions")
	}
}

func TestPermissionValidationFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := Init(path); err != nil {
		t.Fatal(err)
	}
	for _, value := range []Permission{"", "aff", "auth:", "AUTH AFF", "auth:aff:!"} {
		if err := GrantPermissions(4004, value); err == nil {
			t.Fatalf("GrantPermissions(%q) should fail", value)
		}
	}
	if HasPermission(4004, "auth:aff") {
		t.Fatal("invalid grants must not change authorization state")
	}

	invalidPath := filepath.Join(t.TempDir(), "invalid-auth.json")
	invalid := []byte(`{"members":[{"id":5005,"role":99,"permissions":["auth:aff"]}]}`)
	if err := os.WriteFile(invalidPath, invalid, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Init(invalidPath); err == nil {
		t.Fatal("invalid persisted role should prevent startup")
	}
}
