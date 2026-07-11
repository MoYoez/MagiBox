package cloudflare

import (
	"path/filepath"
	"testing"
)

func freshStore(t *testing.T) {
	t.Helper()
	freshStoreAt(t, t.TempDir())
}

// freshStoreAt resets the global store and (re)loads it from dir's
// cloudflare.json, so a second call with the same dir reloads persisted state.
func freshStoreAt(t *testing.T, dir string) {
	t.Helper()
	def = &store{
		creds:     map[string]*Cred{},
		workers:   map[string]*Worker{},
		domains:   map[string]*Domain{},
		failovers: map[string]*FailoverRule{},
	}
	resetFailoverState()
	if err := Init(filepath.Join(dir, "cloudflare.json")); err != nil {
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

func TestDomainLifecycleFieldsPersist(t *testing.T) {
	dir := t.TempDir()
	freshStoreAt(t, dir)
	if err := AddDomain("projA", "life.example.com", ""); err != nil {
		t.Fatal(err)
	}
	if err := MutateDomain("life.example.com", func(d *Domain) error {
		d.PurchasedAt = "2024-01-15"
		d.Usage = "落地页"
		d.DNS = "CNAME → edge.example.net"
		d.Ready = true
		d.ChangedAt = "2024-02-01 12:00"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Reload from disk into a fresh store and confirm the fields survived.
	freshStoreAt(t, dir)
	d, ok := GetDomain("life.example.com")
	if !ok {
		t.Fatal("domain missing after reload")
	}
	if d.PurchasedAt != "2024-01-15" || d.Usage != "落地页" ||
		d.DNS != "CNAME → edge.example.net" || !d.Ready || d.ChangedAt != "2024-02-01 12:00" {
		t.Fatalf("lifecycle fields not persisted: %+v", d)
	}
}

func TestDomainReclassify(t *testing.T) {
	freshStore(t)
	if err := AddDomain("projA", "move.example.com", "old"); err != nil {
		t.Fatal(err)
	}
	if err := MutateDomain("move.example.com", func(d *Domain) error {
		d.Category = "projB"
		d.Sub = "edge"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := ListDomains("projA", ""); len(got) != 0 {
		t.Fatalf("projA should be empty after move, got %+v", got)
	}
	inB := ListDomains("projB", "")
	if len(inB) != 1 || inB[0].Name != "move.example.com" || inB[0].Sub != "edge" {
		t.Fatalf("projB = %+v; want the moved domain with sub=edge", inB)
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
