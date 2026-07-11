package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bytedance/sonic"
)

// apiBase is the Cloudflare API v4 root (a var so tests can point it at a mock).
var apiBase = "https://api.cloudflare.com/client/v4"

// Client talks to the Cloudflare API with one credential's scoped token.
type Client struct {
	accountID string
	token     string
	http      *http.Client
}

// NewClient builds a client for a stored credential.
func NewClient(c *Cred) *Client {
	return &Client{
		accountID: c.AccountID,
		token:     c.Token,
		http:      &http.Client{Timeout: 20 * time.Second},
	}
}

// WorkerDomain is a Worker Custom Domain attachment as returned by the API.
type WorkerDomain struct {
	ID          string `json:"id"`
	Hostname    string `json:"hostname"`
	Service     string `json:"service"`
	Environment string `json:"environment"`
	ZoneID      string `json:"zone_id"`
	ZoneName    string `json:"zone_name"`
}

// zone is the subset of a Cloudflare zone object we need.
type zone struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// envelope is the Cloudflare API response wrapper. Result is decoded by the
// caller into the concrete type.
type envelope struct {
	Success bool            `json:"success"`
	Errors  []apiError      `json:"errors"`
	Result  json.RawMessage `json:"result"`
}

type apiError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// do performs an API request and decodes result into out (out may be nil).
func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := sonic.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, apiBase+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return err
	}
	var env envelope
	if err := sonic.Unmarshal(data, &env); err != nil {
		return fmt.Errorf("HTTP %d,响应无法解析: %s", resp.StatusCode, snippet(data))
	}
	if !env.Success {
		return fmt.Errorf("Cloudflare 拒绝(HTTP %d):%s", resp.StatusCode, apiErrText(env.Errors))
	}
	if out != nil && len(env.Result) > 0 {
		if err := sonic.Unmarshal(env.Result, out); err != nil {
			return fmt.Errorf("解析 result: %w", err)
		}
	}
	return nil
}

// ResolveZoneID finds the zone that owns hostname by listing the credential's
// zones and matching the longest zone name that is a suffix of hostname.
func (c *Client) ResolveZoneID(ctx context.Context, hostname string) (id, name string, err error) {
	var zones []zone
	if err := c.do(ctx, http.MethodGet, "/zones?per_page=50&account.id="+c.accountID, nil, &zones); err != nil {
		return "", "", err
	}
	best := zone{}
	for _, z := range zones {
		if hostname == z.Name || strings.HasSuffix(hostname, "."+z.Name) {
			if len(z.Name) > len(best.Name) {
				best = z
			}
		}
	}
	if best.ID == "" {
		return "", "", fmt.Errorf("这个凭据下没有匹配 %s 的 zone(域名是否在该 Cloudflare 账号里?)", hostname)
	}
	return best.ID, best.Name, nil
}

// ListWorkerDomainsByHostname returns Custom Domain attachments for a hostname
// (usually zero or one).
func (c *Client) ListWorkerDomainsByHostname(ctx context.Context, hostname string) ([]WorkerDomain, error) {
	return c.listWorkerDomains(ctx, "hostname="+url.QueryEscape(hostname))
}

// ListWorkerDomainsByService returns all Custom Domain attachments bound to a
// worker (service).
func (c *Client) ListWorkerDomainsByService(ctx context.Context, service string) ([]WorkerDomain, error) {
	return c.listWorkerDomains(ctx, "service="+url.QueryEscape(service))
}

func (c *Client) listWorkerDomains(ctx context.Context, query string) ([]WorkerDomain, error) {
	var out []WorkerDomain
	path := fmt.Sprintf("/accounts/%s/workers/domains?%s", c.accountID, query)
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// AttachWorkerDomain attaches hostname to a worker (service) as a Custom
// Domain. It is an upsert keyed by hostname: re-pointing an already-attached
// hostname to a different service succeeds.
func (c *Client) AttachWorkerDomain(ctx context.Context, hostname, service, zoneID, env string) (*WorkerDomain, error) {
	if env == "" {
		env = DefaultEnv
	}
	body := map[string]string{
		"hostname":    hostname,
		"service":     service,
		"zone_id":     zoneID,
		"environment": env,
	}
	var out WorkerDomain
	path := fmt.Sprintf("/accounts/%s/workers/domains", c.accountID)
	if err := c.do(ctx, http.MethodPut, path, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteWorkerDomain detaches a Custom Domain by its attachment id.
func (c *Client) DeleteWorkerDomain(ctx context.Context, id string) error {
	path := fmt.Sprintf("/accounts/%s/workers/domains/%s", c.accountID, id)
	return c.do(ctx, http.MethodDelete, path, nil, nil)
}

func apiErrText(errs []apiError) string {
	if len(errs) == 0 {
		return "未知错误"
	}
	parts := make([]string, 0, len(errs))
	for _, e := range errs {
		parts = append(parts, fmt.Sprintf("[%d] %s", e.Code, e.Message))
	}
	return strings.Join(parts, "; ")
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}
