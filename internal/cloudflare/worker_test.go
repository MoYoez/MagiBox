package cloudflare

import (
	"context"
	"net/http"
	"testing"
)

// TestImportWorkerDomains verifies that importing a worker's live Custom
// Domains reconciles them into the record book: new hostnames are inserted
// under the worker's category and marked used+attached, while a pre-existing
// record is marked used+attached without losing its category.
func TestImportWorkerDomains(t *testing.T) {
	freshStore(t)
	if err := AddCred("acct", "acct-id", "tok"); err != nil {
		t.Fatal(err)
	}
	if err := AddWorker("shorttv", "acct", "shorttv-pool"); err != nil {
		t.Fatal(err)
	}
	// One domain already recorded under a different category, unused.
	if err := AddDomain("legacy", "old.example.com", ""); err != nil {
		t.Fatal(err)
	}

	mockCF(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("service"); got != "shorttv" {
			t.Errorf("service query = %q, want shorttv", got)
		}
		ok(w, []WorkerDomain{
			{ID: "d1", Hostname: "new.example.com", Service: "shorttv", ZoneName: "example.com"},
			{ID: "d2", Hostname: "old.example.com", Service: "shorttv", ZoneName: "example.com"},
		})
	})

	results, err := ImportWorkerDomains(context.Background(), "shorttv")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %d, want 2", len(results))
	}

	// New hostname: inserted under the worker's category, used + attached.
	nd, ok := GetDomain("new.example.com")
	if !ok {
		t.Fatal("new.example.com was not inserted")
	}
	if nd.Category != "shorttv-pool" || nd.Status != StatusUsed || nd.Worker != "shorttv" {
		t.Fatalf("new record = %+v; want category shorttv-pool, used, worker shorttv", nd)
	}

	// Pre-existing hostname: marked used + attached, category preserved.
	od, _ := GetDomain("old.example.com")
	if od.Status != StatusUsed || od.Worker != "shorttv" {
		t.Fatalf("old record = %+v; want used + worker shorttv", od)
	}
	if od.Category != "legacy" {
		t.Fatalf("old record category = %q; want preserved as legacy", od.Category)
	}
}

func TestImportWorkerDomainsUnknownWorker(t *testing.T) {
	freshStore(t)
	if _, err := ImportWorkerDomains(context.Background(), "nope"); err == nil {
		t.Fatal("want error for unknown worker")
	}
}
