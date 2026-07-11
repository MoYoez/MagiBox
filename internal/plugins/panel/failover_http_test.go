package panel

import (
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	cf "github.com/moyoez/magibox/internal/cloudflare"
)

// authedPanel mounts the panel, initializes a cloudflare store seeded via seed,
// and returns a logged-in client.
func authedPanel(t *testing.T, seed func()) (*httptest.Server, *http.Client) {
	t.Helper()
	initTestStore(t)
	if err := cf.Init(filepath.Join(t.TempDir(), "cloudflare.json")); err != nil {
		t.Fatal(err)
	}
	if seed != nil {
		seed()
	}
	srv := httptest.NewServer(Plugin{}.HTTPRoutes(nil)[0].Handler)
	t.Cleanup(srv.Close)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}

	code, err := NewCode(time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}
	res, _ := client.Post(srv.URL+"/panel/api/login", "application/json", strings.NewReader(`{"code":"`+code+`"}`))
	if res.StatusCode != 200 {
		t.Fatalf("login = %d", res.StatusCode)
	}
	return srv, client
}

func post(t *testing.T, client *http.Client, url, body string) map[string]any {
	t.Helper()
	res, err := client.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	_ = decodeJSON(res, &out)
	out["_status"] = float64(res.StatusCode)
	return out
}

func TestPanelFailoverAddAppearsInState(t *testing.T) {
	srv, client := authedPanel(t, func() {
		if err := cf.AddCred("acct", "acct-id", "tok"); err != nil {
			t.Fatal(err)
		}
		if err := cf.AddWorker("endpoint", "acct", "pool"); err != nil {
			t.Fatal(err)
		}
	})

	// Create a rule via the panel API.
	r := post(t, client, srv.URL+"/panel/api/cf/failover/add",
		`{"name":"fo","worker":"endpoint","threshold":2,"mode":"auto"}`)
	if r["_status"].(float64) != 200 {
		t.Fatalf("add = %v", r)
	}
	cb, _ := r["callback"].(string)
	if !strings.Contains(cb, cf.FailoverHookPrefix) {
		t.Fatalf("callback %q missing hook prefix", cb)
	}

	// It shows up in state under cf.failovers with its callback URL.
	res, _ := client.Get(srv.URL + "/panel/api/state")
	var st map[string]any
	if err := decodeJSON(res, &st); err != nil {
		t.Fatal(err)
	}
	rules, _ := st["cf"].(map[string]any)["failovers"].([]any)
	if len(rules) != 1 {
		t.Fatalf("failovers in state = %d, want 1", len(rules))
	}
	got := rules[0].(map[string]any)
	if got["worker"] != "endpoint" || got["mode"] != "auto" || got["callback"] == "" {
		t.Errorf("rule out = %v", got)
	}
}

func TestPanelFailoverAddRejectsUnknownWorker(t *testing.T) {
	srv, client := authedPanel(t, nil)
	r := post(t, client, srv.URL+"/panel/api/cf/failover/add",
		`{"name":"fo","worker":"ghost","threshold":2,"mode":"manual"}`)
	if r["_status"].(float64) != http.StatusBadRequest {
		t.Fatalf("add unknown worker = %v, want 400", r["_status"])
	}
}

func TestPanelFailoverApplyRequiresPending(t *testing.T) {
	srv, client := authedPanel(t, func() {
		if err := cf.AddCred("acct", "acct-id", "tok"); err != nil {
			t.Fatal(err)
		}
		if err := cf.AddWorker("endpoint", "acct", "pool"); err != nil {
			t.Fatal(err)
		}
		if _, err := cf.AddFailover("fo", "endpoint", 2, cf.FailoverManual); err != nil {
			t.Fatal(err)
		}
	})
	// No pending switch parked -> apply is a bad request.
	r := post(t, client, srv.URL+"/panel/api/cf/failover/apply", `{"name":"fo"}`)
	if r["_status"].(float64) != http.StatusBadRequest {
		t.Fatalf("apply without pending = %v, want 400", r["_status"])
	}
}
