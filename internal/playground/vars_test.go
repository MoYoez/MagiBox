package playground

import (
	"path/filepath"
	"testing"
)

func TestExpandVars(t *testing.T) {
	if err := InitVars(filepath.Join(t.TempDir(), "vars.json")); err != nil {
		t.Fatal(err)
	}
	if err := SetVar("token", "secret123"); err != nil {
		t.Fatal(err)
	}

	if got := expandVars("Bearer {{token}}"); got != "Bearer secret123" {
		t.Fatalf("替换失败, got %q", got)
	}

	// environment variable fallback
	t.Setenv("MY_KEY", "envval")
	if got := expandVars("{{MY_KEY}}"); got != "envval" {
		t.Fatalf("env 回退失败, got %q", got)
	}

	// variable table takes precedence over an env var of the same name
	t.Setenv("token", "envtoken")
	if got := expandVars("{{token}}"); got != "secret123" {
		t.Fatalf("变量表应优先, got %q", got)
	}

	// runtime args (overrides) have the highest precedence
	if got := expandVarsWith("{{token}}", map[string]string{"token": "rt"}); got != "rt" {
		t.Fatalf("overrides 应最优先, got %q", got)
	}

	// undefined names stay as-is (not silently replaced with an empty string)
	if got := expandVars("{{undefined_xyz}}"); got != "{{undefined_xyz}}" {
		t.Fatalf("未定义应保留, got %q", got)
	}

	// after deletion, falls back to the env var
	if err := DelVar("token"); err != nil {
		t.Fatal(err)
	}
	if got := expandVars("{{token}}"); got != "envtoken" {
		t.Fatalf("删除后应回退 env, got %q", got)
	}
}
