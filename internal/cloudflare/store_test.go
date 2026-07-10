package cloudflare

import (
	"path/filepath"
	"testing"
)

func freshStore(t *testing.T) {
	t.Helper()
	def = &store{
		creds:   map[string]*Cred{},
		workers: map[string]*Worker{},
		domains: map[string]*Domain{},
	}
	if err := Init(filepath.Join(t.TempDir(), "cloudflare.json")); err != nil {
		t.Fatal(err)
	}
}

func TestCredWorkerDomainLifecycle(t *testing.T) {
	freshStore(t)

	if err := AddCred("acct1", "abc123", "token-value"); err != nil {
		t.Fatal(err)
	}
	if err := AddCred("acct1", "x", "y"); err == nil {
		t.Fatal("duplicate cred should fail")
	}
	if err := AddWorker("api", "nope", ""); err == nil {
		t.Fatal("worker under unknown cred should fail")
	}
	if err := AddWorker("api", "acct1", "projA"); err != nil {
		t.Fatal(err)
	}
	// Cred in use cannot be deleted.
	if err := DelCred("acct1"); err == nil {
		t.Fatal("cred deletion should fail while a worker references it")
	}

	if err := AddDomain("projA", "a.example.com", ""); err != nil {
		t.Fatal(err)
	}
	if err := AddDomain("projA", "b.example.com", "edge"); err != nil {
		t.Fatal(err)
	}
	if err := AddDomain("projA", "not a domain", ""); err == nil {
		t.Fatal("invalid domain should fail")
	}

	d, ok := NextUnused("projA")
	if !ok || d.Name != "a.example.com" {
		t.Fatalf("NextUnused = %+v, %v; want a.example.com", d, ok)
	}

	if err := MutateDomain("a.example.com", func(d *Domain) error {
		d.Status = StatusUsed
		d.Worker = "api"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Now only b is free.
	d, ok = NextUnused("projA")
	if !ok || d.Name != "b.example.com" {
		t.Fatalf("NextUnused after use = %+v, %v; want b.example.com", d, ok)
	}

	used := ListDomains("projA", StatusUsed)
	if len(used) != 1 || used[0].Name != "a.example.com" {
		t.Fatalf("ListDomains used = %+v", used)
	}
}

func TestNormalizeStatus(t *testing.T) {
	cases := map[string]string{
		"未使用": StatusUnused, "unused": StatusUnused,
		"已使用": StatusUsed, "used": StatusUsed,
		"ban": StatusBanned, "被ban": StatusBanned, "banned": StatusBanned,
	}
	for in, want := range cases {
		got, ok := NormalizeStatus(in)
		if !ok || got != want {
			t.Errorf("NormalizeStatus(%q) = %q,%v; want %q", in, got, ok, want)
		}
	}
	if _, ok := NormalizeStatus("garbage"); ok {
		t.Error("garbage status should not normalize")
	}
}
