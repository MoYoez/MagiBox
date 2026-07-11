package cf

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bytedance/sonic"
	cf "github.com/moyoez/magibox/internal/cloudflare"
)

func mustJSON(v any) []byte {
	b, err := sonic.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func freshCF(t *testing.T) {
	t.Helper()
	if err := cf.Init(filepath.Join(t.TempDir(), "cloudflare.json")); err != nil {
		t.Fatal(err)
	}
}

func downBody(domain string) string {
	return fmt.Sprintf(`{"heartbeat":{"status":0},"monitor":{"name":%q},"msg":"down"}`, domain)
}

func upBody(domain string) string {
	return fmt.Sprintf(`{"heartbeat":{"status":1},"monitor":{"name":%q},"msg":"up"}`, domain)
}

func post(rule *cf.FailoverRule, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, hookPrefix+rule.Token, strings.NewReader(body))
	rec := httptest.NewRecorder()
	handleHook(nil, rec, req) // nil bot -> notifications log instead of pushing
	return rec
}

func TestHandleHookUnknownToken(t *testing.T) {
	freshCF(t)
	req := httptest.NewRequest(http.MethodPost, hookPrefix+"deadbeef", strings.NewReader("{}"))
	rec := httptest.NewRecorder()
	handleHook(nil, rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404", rec.Code)
	}
}

func TestManualRuleParksPendingUntilApply(t *testing.T) {
	freshCF(t)
	if err := cf.AddCred("acct", "acct-id", "tok"); err != nil {
		t.Fatal(err)
	}
	if err := cf.AddWorker("endpoint", "acct", "pool"); err != nil {
		t.Fatal(err)
	}
	if err := cf.AddDomain("pool", "good.example.com", ""); err != nil {
		t.Fatal(err)
	}
	rule, err := cf.AddFailover("fo", "endpoint", 2, cf.FailoverManual)
	if err != nil {
		t.Fatal(err)
	}

	// Below threshold: nothing parked.
	post(rule, downBody("bad.example.com"))
	if _, ok := cf.Pending("fo"); ok {
		t.Fatal("should not be pending after a single down (threshold 2)")
	}
	// At threshold: a manual rule parks a pending switch but does not act.
	post(rule, downBody("bad.example.com"))
	pend, ok := cf.Pending("fo")
	if !ok || pend != "bad.example.com" {
		t.Fatalf("pending = %q/%v, want bad.example.com parked", pend, ok)
	}
}

func TestUpClearsStreak(t *testing.T) {
	freshCF(t)
	if err := cf.AddCred("acct", "acct-id", "tok"); err != nil {
		t.Fatal(err)
	}
	if err := cf.AddWorker("endpoint", "acct", "pool"); err != nil {
		t.Fatal(err)
	}
	rule, err := cf.AddFailover("fo", "endpoint", 2, cf.FailoverManual)
	if err != nil {
		t.Fatal(err)
	}
	post(rule, downBody("bad.example.com")) // 1/2
	post(rule, upBody("bad.example.com"))   // recovered -> reset
	post(rule, downBody("bad.example.com")) // 1/2 again, not 2/2
	if _, ok := cf.Pending("fo"); ok {
		t.Fatal("streak should have reset after up; not pending yet")
	}
}

func TestAutoRuleSwitchesAtThreshold(t *testing.T) {
	freshCF(t)
	if err := cf.AddCred("acct", "acct-id", "tok"); err != nil {
		t.Fatal(err)
	}
	if err := cf.AddWorker("endpoint", "acct", "pool"); err != nil {
		t.Fatal(err)
	}
	if err := cf.AddDomain("pool", "bad.example.com", ""); err != nil {
		t.Fatal(err)
	}
	if err := cf.AddDomain("pool", "good.example.com", ""); err != nil {
		t.Fatal(err)
	}
	if err := cf.SetDomainField("bad.example.com", "status", "used"); err != nil {
		t.Fatal(err)
	}
	rule, err := cf.AddFailover("fo", "endpoint", 1, cf.FailoverAuto)
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.RawQuery, "hostname=bad"):
			writeResult(w, []map[string]any{{"id": "d-bad", "hostname": "bad.example.com", "service": "endpoint"}})
		case r.Method == http.MethodDelete:
			writeResult(w, map[string]any{})
		case strings.HasPrefix(r.URL.Path, "/zones"):
			writeResult(w, []map[string]any{{"id": "z1", "name": "example.com"}})
		case r.Method == http.MethodPut:
			writeResult(w, map[string]any{"id": "d-good", "hostname": "good.example.com", "service": "endpoint"})
		default:
			writeResult(w, []map[string]any{})
		}
	}))
	defer srv.Close()
	defer cf.SetAPIBaseForTest(srv.URL)()

	post(rule, downBody("bad.example.com")) // threshold 1 -> auto switch now

	if d, _ := cf.GetDomain("bad.example.com"); d.Status != cf.StatusBanned {
		t.Errorf("bad status = %s, want banned", d.Status)
	}
	if d, _ := cf.GetDomain("good.example.com"); d.Status != cf.StatusUsed {
		t.Errorf("good status = %s, want used", d.Status)
	}
}

func writeResult(w http.ResponseWriter, result any) {
	_, _ = w.Write(mustJSON(map[string]any{"success": true, "errors": []any{}, "result": result}))
}
