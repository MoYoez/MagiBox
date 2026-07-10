package panel

import (
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// mountPanel initializes the store and serves the panel mux.
func mountPanel(t *testing.T) (*httptest.Server, *http.Client) {
	t.Helper()
	initTestStore(t)
	routes := Plugin{}.HTTPRoutes(nil)
	if len(routes) != 1 {
		t.Fatalf("want 1 route, got %d", len(routes))
	}
	srv := httptest.NewServer(routes[0].Handler)
	t.Cleanup(srv.Close)
	jar, _ := cookiejar.New(nil)
	return srv, &http.Client{Jar: jar}
}

func TestPanelOneTimeLoginFlow(t *testing.T) {
	srv, client := mountPanel(t)

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

	// A bogus code is rejected.
	res, _ = client.Post(srv.URL+"/panel/api/login", "application/json", strings.NewReader(`{"code":"nope-nope-nope-nope"}`))
	if res.StatusCode != 401 {
		t.Fatalf("bad code = %d, want 401", res.StatusCode)
	}

	// Mint a one-time code (as an admin would via the bot) and log in with it.
	code, err := NewCode(time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}
	res, _ = client.Post(srv.URL+"/panel/api/login", "application/json", strings.NewReader(`{"code":"`+code+`"}`))
	if res.StatusCode != 200 || len(res.Cookies()) == 0 {
		t.Fatalf("login = %d, cookies=%d; want 200 with cookie", res.StatusCode, len(res.Cookies()))
	}

	// The same code cannot be reused (single-use) — from a fresh client.
	jar2, _ := cookiejar.New(nil)
	res, _ = (&http.Client{Jar: jar2}).Post(srv.URL+"/panel/api/login", "application/json", strings.NewReader(`{"code":"`+code+`"}`))
	if res.StatusCode != 401 {
		t.Fatalf("reused code = %d, want 401", res.StatusCode)
	}

	// The logged-in client can now read state.
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
