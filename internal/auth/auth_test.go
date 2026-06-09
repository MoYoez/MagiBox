package auth

import (
	"os"
	"path/filepath"
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
