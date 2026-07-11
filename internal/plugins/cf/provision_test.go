package cf

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	cf "github.com/moyoez/magibox/internal/cloudflare"
	kuma "github.com/moyoez/magibox/internal/kuma"
)

// cfMock returns a Cloudflare API mock that answers the calls BindDomain makes:
// list-by-hostname (none), zones (example.com), and the PUT attach.
func cfMock(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/zones"):
			writeResult(w, []map[string]any{{"id": "z1", "name": "example.com"}})
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/workers/domains"):
			writeResult(w, []map[string]any{}) // no existing attachment
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/workers/domains"):
			writeResult(w, map[string]any{"id": "d1", "hostname": "new.example.com", "service": "endpoint"})
		default:
			writeResult(w, []map[string]any{})
		}
	}))
}

// kumaRecorder records the Kuma wrapper calls provision makes so the test can
// assert the full monitor+notification wiring happened.
type kumaRecorder struct {
	mu             sync.Mutex
	createdMonitor bool
	createdNotif   bool
	setNotif       bool
	boundName      string
	boundNotifIDs  []any
}

func kumaMock(t *testing.T, rec *kumaRecorder) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.mu.Lock()
		defer rec.mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/monitors":
			_, _ = io.WriteString(w, `[]`) // no existing monitor -> create
		case r.Method == http.MethodPost && r.URL.Path == "/monitors":
			rec.createdMonitor = true
			_ = json.NewEncoder(w).Encode(map[string]any{"monitorId": 55})
		case r.Method == http.MethodGet && r.URL.Path == "/notifications":
			_, _ = io.WriteString(w, `[]`) // no existing notification -> create
		case r.Method == http.MethodPost && r.URL.Path == "/notifications":
			rec.createdNotif = true
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 77})
		case r.Method == http.MethodPut && r.URL.Path == "/monitors/set-notifications":
			rec.setNotif = true
			rec.boundName = r.URL.Query().Get("name_pattern")
			body, _ := io.ReadAll(r.Body)
			var m map[string]any
			_ = json.Unmarshal(body, &m)
			rec.boundNotifIDs, _ = m["notification_ids"].([]any)
			_ = json.NewEncoder(w).Encode(map[string]any{"total": 1, "successful": 1, "failed": 0})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestProvisionEndToEnd(t *testing.T) {
	freshCF(t)
	if err := kuma.Init(t.TempDir() + "/kuma.json"); err != nil {
		t.Fatal(err)
	}
	if err := cf.AddCred("acct", "acct-id", "tok"); err != nil {
		t.Fatal(err)
	}
	if err := cf.AddWorker("endpoint", "acct", "pool"); err != nil {
		t.Fatal(err)
	}
	if err := cf.AddDomain("pool", "new.example.com", ""); err != nil {
		t.Fatal(err)
	}
	rule, err := cf.AddFailover("fo", "endpoint", 2, cf.FailoverManual)
	if err != nil {
		t.Fatal(err)
	}

	cfSrv := cfMock(t)
	defer cfSrv.Close()
	defer cf.SetAPIBaseForTest(cfSrv.URL)()

	rec := &kumaRecorder{}
	kSrv := kumaMock(t, rec)
	defer kSrv.Close()
	if err := kuma.AddCred("k", kSrv.URL, ""); err != nil {
		t.Fatal(err)
	}
	if err := cf.MutateFailover(rule.Name, func(r *cf.FailoverRule) error {
		r.KumaCred = "k"
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Auto-pick from the pool (no domain given).
	msg := provision(context.Background(), "endpoint", "")
	if !strings.Contains(msg, "已全自动纳管") {
		t.Fatalf("provision message = %q, want success", msg)
	}

	// The domain is live and marked used.
	if d, ok := cf.GetDomain("new.example.com"); !ok || d.Status != cf.StatusUsed || d.Worker != "endpoint" {
		t.Fatalf("domain record = %+v, want used + endpoint", d)
	}
	// The full Kuma wiring ran.
	if !rec.createdMonitor || !rec.createdNotif || !rec.setNotif {
		t.Fatalf("kuma calls: monitor=%v notif=%v set=%v, want all true",
			rec.createdMonitor, rec.createdNotif, rec.setNotif)
	}
	if rec.boundName != "new.example.com" || len(rec.boundNotifIDs) != 1 || rec.boundNotifIDs[0] != float64(77) {
		t.Fatalf("bound name=%q notifs=%v, want new.example.com / [77]", rec.boundName, rec.boundNotifIDs)
	}
}

func TestProvisionRequiresRule(t *testing.T) {
	freshCF(t)
	if err := cf.AddCred("acct", "acct-id", "tok"); err != nil {
		t.Fatal(err)
	}
	if err := cf.AddWorker("endpoint", "acct", "pool"); err != nil {
		t.Fatal(err)
	}
	// No failover rule -> provision must refuse and say so.
	msg := provision(context.Background(), "endpoint", "")
	if !strings.Contains(msg, "还没有 failover 规则") {
		t.Fatalf("msg = %q, want a missing-rule refusal", msg)
	}
}

func TestProvisionRequiresKumaCred(t *testing.T) {
	freshCF(t)
	if err := cf.AddCred("acct", "acct-id", "tok"); err != nil {
		t.Fatal(err)
	}
	if err := cf.AddWorker("endpoint", "acct", "pool"); err != nil {
		t.Fatal(err)
	}
	if _, err := cf.AddFailover("fo", "endpoint", 2, cf.FailoverManual); err != nil {
		t.Fatal(err)
	}
	// Rule exists but no Kuma cred bound -> refuse.
	msg := provision(context.Background(), "endpoint", "")
	if !strings.Contains(msg, "还没绑定 Kuma 凭据") {
		t.Fatalf("msg = %q, want a missing-Kuma-cred refusal", msg)
	}
}
