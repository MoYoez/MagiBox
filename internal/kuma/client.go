package kuma

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/bytedance/sonic"
)

// Client talks to an Uptime Kuma REST wrapper for one credential.
type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

// NewClient builds a client for a stored credential.
func NewClient(c *Cred) *Client {
	return &Client{
		baseURL: strings.TrimRight(c.BaseURL, "/"),
		apiKey:  c.APIKey,
		http:    &http.Client{Timeout: 20 * time.Second},
	}
}

// Monitor is the subset of a Kuma monitor we surface.
type Monitor struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	URL    string `json:"url"`
	Active bool   `json:"active"`
}

// createResp tolerates the varied create-monitor response shapes wrappers
// return (id / monitorId, plus an optional message).
type createResp struct {
	ID        int    `json:"id"`
	MonitorID int    `json:"monitorId"`
	Msg       string `json:"msg"`
}

// CreateMonitor creates an HTTP monitor named name pointing at url. It returns
// the new monitor id when the wrapper reports one (0 if it does not) plus any
// message.
func (c *Client) CreateMonitor(ctx context.Context, name, url string) (id int, msg string, err error) {
	body := map[string]any{
		"type":     "http",
		"name":     name,
		"url":      url,
		"interval": 60,
	}
	data, err := c.do(ctx, http.MethodPost, "/monitors", body)
	if err != nil {
		return 0, "", err
	}
	var r createResp
	_ = sonic.Unmarshal(data, &r)
	if r.ID != 0 {
		return r.ID, r.Msg, nil
	}
	return r.MonitorID, r.Msg, nil
}

// ListMonitors returns the wrapper's monitors, tolerating either a bare array
// or an object wrapping them under "monitors".
func (c *Client) ListMonitors(ctx context.Context) ([]Monitor, error) {
	data, err := c.do(ctx, http.MethodGet, "/monitors", nil)
	if err != nil {
		return nil, err
	}
	var arr []Monitor
	if err := sonic.Unmarshal(data, &arr); err == nil {
		return arr, nil
	}
	var wrapped struct {
		Monitors []Monitor `json:"monitors"`
	}
	if err := sonic.Unmarshal(data, &wrapped); err != nil {
		return nil, fmt.Errorf("无法解析监控列表: %s", snippet(data))
	}
	return wrapped.Monitors, nil
}

// DeleteMonitor removes a monitor by id.
func (c *Client) DeleteMonitor(ctx context.Context, id int) error {
	_, err := c.do(ctx, http.MethodDelete, fmt.Sprintf("/monitors/%d", id), nil)
	return err
}

// FindMonitorByName returns the first monitor whose name matches exactly, or
// false when none does. Used to reuse a monitor the user created by hand rather
// than creating a duplicate.
func (c *Client) FindMonitorByName(ctx context.Context, name string) (Monitor, bool, error) {
	monitors, err := c.ListMonitors(ctx)
	if err != nil {
		return Monitor{}, false, err
	}
	for _, m := range monitors {
		if m.Name == name {
			return m, true, nil
		}
	}
	return Monitor{}, false, nil
}

// Notification is the subset of a Kuma notification we surface.
type Notification struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// CreateWebhookNotification creates a webhook notification that POSTs Kuma's
// default JSON payload (monitor + heartbeat objects) to url — the shape the cf
// failover receiver parses. It returns the new notification id (0 if the
// wrapper does not report one).
func (c *Client) CreateWebhookNotification(ctx context.Context, name, url string) (id int, err error) {
	body := map[string]any{
		"name":               name,
		"type":               "webhook",
		"webhookURL":         url,
		"webhookContentType": "json",
	}
	data, err := c.do(ctx, http.MethodPost, "/notifications", body)
	if err != nil {
		return 0, err
	}
	var r createResp
	_ = sonic.Unmarshal(data, &r)
	if r.ID != 0 {
		return r.ID, nil
	}
	return r.MonitorID, nil
}

// ListNotifications returns the wrapper's notifications, tolerating either a
// bare array or an object wrapping them under "notifications".
func (c *Client) ListNotifications(ctx context.Context) ([]Notification, error) {
	data, err := c.do(ctx, http.MethodGet, "/notifications", nil)
	if err != nil {
		return nil, err
	}
	var arr []Notification
	if err := sonic.Unmarshal(data, &arr); err == nil {
		return arr, nil
	}
	var wrapped struct {
		Notifications []Notification `json:"notifications"`
	}
	if err := sonic.Unmarshal(data, &wrapped); err != nil {
		return nil, fmt.Errorf("无法解析通知列表: %s", snippet(data))
	}
	return wrapped.Notifications, nil
}

// FindNotificationByName returns the first notification whose name matches
// exactly, or false when none does (so provision can reuse it).
func (c *Client) FindNotificationByName(ctx context.Context, name string) (Notification, bool, error) {
	ns, err := c.ListNotifications(ctx)
	if err != nil {
		return Notification{}, false, err
	}
	for _, n := range ns {
		if n.Name == name {
			return n, true, nil
		}
	}
	return Notification{}, false, nil
}

// SetMonitorNotifications attaches the given notification ids to a monitor
// (replacing whatever it had). The keithah wrapper exposes this as
// PUT /monitors/set-notifications with {monitor_id, notification_ids}.
func (c *Client) SetMonitorNotifications(ctx context.Context, monitorID int, notificationIDs []int) error {
	body := map[string]any{
		"monitor_id":       monitorID,
		"notification_ids": notificationIDs,
	}
	_, err := c.do(ctx, http.MethodPut, "/monitors/set-notifications", body)
	return err
}

// do performs a request and returns the raw response body, treating any
// non-2xx as an error.
func (c *Client) do(ctx context.Context, method, path string, body any) ([]byte, error) {
	var rdr io.Reader
	if body != nil {
		b, err := sonic.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rdr)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("Kuma 包装器返回 HTTP %d:%s", resp.StatusCode, snippet(data))
	}
	return data, nil
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}
