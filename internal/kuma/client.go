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
