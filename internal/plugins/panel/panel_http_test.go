package panel

import (
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
)

// mountPanel builds the panel mux with PANEL_CODE set and serves it.
func mountPanel(t *testing.T, code string) (*httptest.Server, *http.Client) {
	t.Helper()
	t.Setenv("PANEL_CODE", code)
	routes := Plugin{}.HTTPRoutes(nil)
	if len(routes) != 1 {
		t.Fatalf("want 1 route, got %d", len(routes))
	}
	srv := httptest.NewServer(routes[0].Handler)
	t.Cleanup(srv.Close)
	jar, _ := cookiejar.New(nil)
	return srv, &http.Client{Jar: jar}
}

func TestPanelDisabledWithoutCode(t *testing.T) {
	t.Setenv("PANEL_CODE", "")
	if routes := (Plugin{}).HTTPRoutes(nil); routes != nil {
		t.Fatalf("panel should be disabled without PANEL_CODE, got %d routes", len(routes))
	}
}

func TestPanelLoginFlow(t *testing.T) {
	srv, client := mountPanel(t, "s3cr3t")

	// The SPA shell is public.
	res, err := client.Get(srv.URL + "/panel/")
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 || !strings.Contains(res.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("GET /panel/ = %d %s", res.StatusCode, res.Header.Get("Content-Type"))
	}

	// State is gated.
	res, _ = client.Get(srv.URL + "/panel/api/state")
	if res.StatusCode != 401 {
		t.Fatalf("unauthenticated state = %d, want 401", res.StatusCode)
	}

	// Wrong code is rejected.
	res, _ = client.Post(srv.URL+"/panel/api/login", "application/json", strings.NewReader(`{"code":"nope"}`))
	if res.StatusCode != 401 {
		t.Fatalf("wrong code = %d, want 401", res.StatusCode)
	}

	// Correct code sets a session cookie.
	res, _ = client.Post(srv.URL+"/panel/api/login", "application/json", strings.NewReader(`{"code":"s3cr3t"}`))
	if res.StatusCode != 200 {
		t.Fatalf("login = %d, want 200", res.StatusCode)
	}
	if len(res.Cookies()) == 0 {
		t.Fatal("login did not set a cookie")
	}

	// Now state works and carries the expected top-level keys.
	res, _ = client.Get(srv.URL + "/panel/api/state")
	if res.StatusCode != 200 {
		t.Fatalf("authed state = %d, want 200", res.StatusCode)
	}
	var body map[string]any
	if err := decodeJSON(res, &body); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"cf", "kuma", "uptime"} {
		if _, ok := body[k]; !ok {
			t.Errorf("state missing %q key", k)
		}
	}

	// Logout clears the session.
	res, _ = client.Post(srv.URL+"/panel/api/logout", "application/json", nil)
	if res.StatusCode != 200 {
		t.Fatalf("logout = %d", res.StatusCode)
	}
	res, _ = client.Get(srv.URL + "/panel/api/state")
	if res.StatusCode != 401 {
		t.Fatalf("state after logout = %d, want 401", res.StatusCode)
	}
}

func decodeJSON(res *http.Response, v any) error {
	defer res.Body.Close()
	return json.NewDecoder(res.Body).Decode(v)
}
