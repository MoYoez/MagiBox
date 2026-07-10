package cloudflare

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// mockCF stands up a fake Cloudflare API and points apiBase at it.
func mockCF(t *testing.T, h http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(h)
	prev := apiBase
	apiBase = srv.URL
	t.Cleanup(func() {
		apiBase = prev
		srv.Close()
	})
}

func ok(w http.ResponseWriter, result any) {
	_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "errors": []any{}, "result": result})
}

func TestResolveZoneIDLongestSuffix(t *testing.T) {
	mockCF(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/zones") {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("auth header = %q", got)
		}
		ok(w, []zone{
			{ID: "z-apex", Name: "example.com"},
			{ID: "z-sub", Name: "sub.example.com"},
			{ID: "z-other", Name: "other.com"},
		})
	})
	c := NewClient(&Cred{AccountID: "acct", Token: "tok"})
	id, name, err := c.ResolveZoneID(context.Background(), "api.sub.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if id != "z-sub" || name != "sub.example.com" {
		t.Fatalf("zone = %s/%s; want z-sub/sub.example.com (longest suffix)", id, name)
	}
}

func TestResolveZoneIDNoMatch(t *testing.T) {
	mockCF(t, func(w http.ResponseWriter, r *http.Request) {
		ok(w, []zone{{ID: "z1", Name: "other.com"}})
	})
	c := NewClient(&Cred{AccountID: "acct", Token: "tok"})
	if _, _, err := c.ResolveZoneID(context.Background(), "api.example.com"); err == nil {
		t.Fatal("want error when no zone matches")
	}
}

func TestAttachWorkerDomainSendsBody(t *testing.T) {
	mockCF(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("method = %s, want PUT", r.Method)
		}
		body, _ := io.ReadAll(r.Body)
		var got map[string]string
		_ = json.Unmarshal(body, &got)
		if got["hostname"] != "a.example.com" || got["service"] != "api" ||
			got["zone_id"] != "z1" || got["environment"] != "production" {
			t.Errorf("attach body = %v", got)
		}
		ok(w, WorkerDomain{ID: "d1", Hostname: got["hostname"], Service: got["service"], ZoneID: got["zone_id"]})
	})
	c := NewClient(&Cred{AccountID: "acct", Token: "tok"})
	wd, err := c.AttachWorkerDomain(context.Background(), "a.example.com", "api", "z1", "")
	if err != nil {
		t.Fatal(err)
	}
	if wd.ID != "d1" {
		t.Fatalf("attached id = %s, want d1", wd.ID)
	}
}

func TestAPIErrorSurfaced(t *testing.T) {
	mockCF(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": false,
			"errors":  []apiError{{Code: 10000, Message: "Authentication error"}},
		})
	})
	c := NewClient(&Cred{AccountID: "acct", Token: "bad"})
	_, err := c.ListWorkerDomainsByHostname(context.Background(), "a.example.com")
	if err == nil || !strings.Contains(err.Error(), "Authentication error") {
		t.Fatalf("err = %v, want Cloudflare auth error surfaced", err)
	}
}
