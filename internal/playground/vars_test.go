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

func TestMissingVars(t *testing.T) {
	if err := InitVars(filepath.Join(t.TempDir(), "vars.json")); err != nil {
		t.Fatal(err)
	}
	g := &Group{
		BaseURL:  "https://api.x",
		Endpoint: "/v1?city={{city}}&key={{mv_api_key}}",
		Headers:  map[string]string{"Authorization": "Bearer {{mv_token}}"},
	}

	// nothing resolvable → all three reported (sorted, deduped)
	if got := MissingVars(g, nil); len(got) != 3 ||
		got[0] != "city" || got[1] != "mv_api_key" || got[2] != "mv_token" {
		t.Fatalf("missing = %v, 期望 [city mv_api_key mv_token]", got)
	}

	// runtime override fills one
	if got := MissingVars(g, map[string]string{"city": "sh"}); len(got) != 2 {
		t.Fatalf("override 后 missing = %v", got)
	}

	// variable table fills another
	if err := SetVar("mv_token", "x"); err != nil {
		t.Fatal(err)
	}
	if got := MissingVars(g, map[string]string{"city": "sh"}); len(got) != 1 || got[0] != "mv_api_key" {
		t.Fatalf("变量表后 missing = %v", got)
	}

	// env var fills the last one
	t.Setenv("mv_api_key", "k")
	if got := MissingVars(g, map[string]string{"city": "sh"}); len(got) != 0 {
		t.Fatalf("全部可解析时应为空, got %v", got)
	}
}
