package cloudflare

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// seedWorkerWithPool registers a cred + worker bound to category "pool" and
// adds the given domains to that category (all unused).
func seedWorkerWithPool(t *testing.T, worker string, domains ...string) {
	t.Helper()
	if err := AddCred("acct", "acct-id", "tok"); err != nil {
		t.Fatal(err)
	}
	if err := AddWorker(worker, "acct", "pool"); err != nil {
		t.Fatal(err)
	}
	for _, d := range domains {
		if err := AddDomain("pool", d, ""); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRecordDownThreshold(t *testing.T) {
	freshStore(t)
	rule := &FailoverRule{Name: "r", Worker: "w", Threshold: 2}

	if _, fire := RecordDown(rule, "a.example.com"); fire {
		t.Fatal("first down should not fire (threshold 2)")
	}
	if _, fire := RecordDown(rule, "a.example.com"); !fire {
		t.Fatal("second down should fire (threshold 2)")
	}
	// A recovery resets the streak.
	RecordUp("a.example.com")
	if _, fire := RecordDown(rule, "a.example.com"); fire {
		t.Fatal("down after recovery should not fire")
	}
}

func TestSwitchWorkerDomain(t *testing.T) {
	freshStore(t)
	seedWorkerWithPool(t, "endpoint", "bad.example.com", "good.example.com")
	// bad is the one currently in use.
	if err := SetDomainField("bad.example.com", "status", "used"); err != nil {
		t.Fatal(err)
	}

	var deleted bool
	mockCF(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.RawQuery, "hostname="):
			// detachHostname looks up the bad domain's attachment.
			ok(w, []WorkerDomain{{ID: "d-bad", Hostname: "bad.example.com", Service: "endpoint"}})
		case r.Method == http.MethodDelete:
			deleted = true
			ok(w, map[string]any{})
		case strings.HasPrefix(r.URL.Path, "/zones"):
			ok(w, []zone{{ID: "z1", Name: "example.com"}})
		case r.Method == http.MethodPut:
			// BindDomain first checks existing attachments (GET) then PUTs.
			ok(w, WorkerDomain{ID: "d-good", Hostname: "good.example.com", Service: "endpoint", ZoneID: "z1"})
		case r.Method == http.MethodGet:
			// BindDomain's conflict check for good.example.com: none.
			ok(w, []WorkerDomain{})
		default:
			ok(w, map[string]any{})
		}
	})

	res, err := SwitchWorkerDomain(context.Background(), "endpoint", "bad.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if !deleted {
		t.Error("expected bad domain to be detached (DELETE)")
	}
	if res.New != "good.example.com" {
		t.Errorf("switched to %s, want good.example.com", res.New)
	}

	// bad domain is now banned and unbound.
	if d, _ := GetDomain("bad.example.com"); d.Status != StatusBanned {
		t.Errorf("bad domain status = %s, want banned", d.Status)
	}
	// good domain is now used and bound to the worker.
	if d, _ := GetDomain("good.example.com"); d.Status != StatusUsed || d.Worker != "endpoint" {
		t.Errorf("good domain = %+v, want used/endpoint", d)
	}
}

func TestSwitchWorkerDomainEmptyPool(t *testing.T) {
	freshStore(t)
	seedWorkerWithPool(t, "endpoint", "bad.example.com")
	if err := SetDomainField("bad.example.com", "status", "used"); err != nil {
		t.Fatal(err)
	}
	// No unused domain left in the pool -> must error before touching CF.
	_, err := SwitchWorkerDomain(context.Background(), "endpoint", "bad.example.com")
	if err == nil || !strings.Contains(err.Error(), "没有") {
		t.Fatalf("err = %v, want empty-pool error", err)
	}
}

func TestFailoverCRUDPersist(t *testing.T) {
	dir := t.TempDir()
	freshStoreAt(t, dir)
	if err := AddCred("acct", "acct-id", "tok"); err != nil {
		t.Fatal(err)
	}
	if err := AddWorker("endpoint", "acct", "pool"); err != nil {
		t.Fatal(err)
	}
	r, err := AddFailover("fo", "endpoint", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if r.Threshold != DefaultThreshold || r.Mode != FailoverManual {
		t.Errorf("defaults not applied: %+v", r)
	}
	if _, ok := GetFailoverByToken(r.Token); !ok {
		t.Error("token lookup failed")
	}

	// Reload from disk and confirm it survived.
	freshStoreAt(t, dir)
	got, ok := GetFailover("fo")
	if !ok {
		t.Fatal("rule did not persist")
	}
	if got.Worker != "endpoint" || got.Token != r.Token {
		t.Errorf("reloaded rule = %+v", got)
	}
}

func TestAddFailoverUnknownWorker(t *testing.T) {
	freshStore(t)
	if _, err := AddFailover("fo", "ghost", 2, FailoverAuto); err == nil {
		t.Fatal("want error for unknown worker")
	}
}
