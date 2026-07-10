package kuma

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCreateMonitorPostsBodyAndAuth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/monitors" {
			t.Errorf("got %s %s, want POST /monitors", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer secret" {
			t.Errorf("auth header = %q, want Bearer secret", got)
		}
		body, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(body, &m)
		if m["name"] != "a.example.com" || m["url"] != "https://a.example.com" || m["type"] != "http" {
			t.Errorf("create body = %v", m)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"msg": "Added Successfully.", "monitorId": 42})
	}))
	defer srv.Close()

	c := NewClient(&Cred{BaseURL: srv.URL, APIKey: "secret"})
	id, msg, err := c.CreateMonitor(context.Background(), "a.example.com", "https://a.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if id != 42 || !strings.Contains(msg, "Added") {
		t.Fatalf("id=%d msg=%q, want 42 / Added", id, msg)
	}
}

func TestCreateMonitorNoAuthHeaderWhenKeyEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := r.Header["Authorization"]; ok {
			t.Error("Authorization header should be absent when API key is empty")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 7})
	}))
	defer srv.Close()

	c := NewClient(&Cred{BaseURL: srv.URL})
	id, _, err := c.CreateMonitor(context.Background(), "x.example.com", "https://x.example.com")
	if err != nil || id != 7 {
		t.Fatalf("id=%d err=%v, want 7 / nil", id, err)
	}
}

func TestListMonitorsBareArrayAndWrapped(t *testing.T) {
	for _, tc := range []struct {
		name string
		resp string
	}{
		{"bare array", `[{"id":1,"name":"a","url":"https://a"}]`},
		{"wrapped", `{"monitors":[{"id":1,"name":"a","url":"https://a"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, tc.resp)
			}))
			defer srv.Close()
			ms, err := NewClient(&Cred{BaseURL: srv.URL}).ListMonitors(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(ms) != 1 || ms[0].ID != 1 || ms[0].Name != "a" {
				t.Fatalf("monitors = %+v", ms)
			}
		})
	}
}

func TestNon2xxSurfacesError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":"unauthorized"}`)
	}))
	defer srv.Close()
	_, err := NewClient(&Cred{BaseURL: srv.URL}).ListMonitors(context.Background())
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("err = %v, want HTTP 401 surfaced", err)
	}
}
