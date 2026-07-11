package kuma

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCreateWebhookNotificationBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/notifications" {
			t.Errorf("got %s %s, want POST /notifications", r.Method, r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(body, &m)
		if m["type"] != "webhook" || m["webhookURL"] != "https://hook.example/x" || m["webhookContentType"] != "json" {
			t.Errorf("notification body = %v", m)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 9})
	}))
	defer srv.Close()

	id, err := NewClient(&Cred{BaseURL: srv.URL}).CreateWebhookNotification(
		context.Background(), "magibox-r", "https://hook.example/x")
	if err != nil || id != 9 {
		t.Fatalf("id=%d err=%v, want 9 / nil", id, err)
	}
}

func TestFindNotificationByName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"notifications":[{"id":3,"name":"magibox-r"},{"id":4,"name":"other"}]}`)
	}))
	defer srv.Close()

	c := NewClient(&Cred{BaseURL: srv.URL})
	n, ok, err := c.FindNotificationByName(context.Background(), "magibox-r")
	if err != nil || !ok || n.ID != 3 {
		t.Fatalf("n=%+v ok=%v err=%v, want id 3 / true / nil", n, ok, err)
	}
	_, ok, err = c.FindNotificationByName(context.Background(), "nope")
	if err != nil || ok {
		t.Fatalf("ok=%v err=%v, want false / nil for missing", ok, err)
	}
}

func TestFindMonitorByName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `[{"id":11,"name":"a.example.com","url":"https://a.example.com"}]`)
	}))
	defer srv.Close()

	c := NewClient(&Cred{BaseURL: srv.URL})
	m, ok, err := c.FindMonitorByName(context.Background(), "a.example.com")
	if err != nil || !ok || m.ID != 11 {
		t.Fatalf("m=%+v ok=%v err=%v, want id 11 / true / nil", m, ok, err)
	}
}

func TestSetMonitorNotificationsBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/monitors/set-notifications" {
			t.Errorf("got %s %s, want PUT /monitors/set-notifications", r.Method, r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(body, &m)
		if m["monitor_id"] != float64(11) {
			t.Errorf("monitor_id = %v, want 11", m["monitor_id"])
		}
		ids, _ := m["notification_ids"].([]any)
		if len(ids) != 1 || ids[0] != float64(9) {
			t.Errorf("notification_ids = %v, want [9]", m["notification_ids"])
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer srv.Close()

	err := NewClient(&Cred{BaseURL: srv.URL}).SetMonitorNotifications(context.Background(), 11, []int{9})
	if err != nil {
		t.Fatal(err)
	}
}
