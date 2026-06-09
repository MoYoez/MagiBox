package playground

import (
	"context"
	"io"
	"net/http"
	"strings"
	"time"
)

var client = &http.Client{Timeout: 15 * time.Second}

const maxRespBytes = 20 << 20 // 20MB

// Response is the result of one execution.
type Response struct {
	Code   int
	Header http.Header
	Body   []byte
}

// Execute issues an HTTP request per the group config. overrides are
// per-call temporary variables (taking precedence over the persistent
// variable table and environment variables), used for runtime arguments.
func Execute(ctx context.Context, g *Group, overrides map[string]string) (*Response, error) {
	method := g.Method
	if method == "" {
		method = http.MethodGet
	}
	var rb io.Reader
	if g.Body != "" {
		rb = strings.NewReader(expandVarsWith(g.Body, overrides))
	}
	url := buildURL(expandVarsWith(g.BaseURL, overrides), expandVarsWith(g.Endpoint, overrides))
	req, err := http.NewRequestWithContext(ctx, strings.ToUpper(method), url, rb)
	if err != nil {
		return nil, err
	}
	for k, v := range g.Headers {
		req.Header.Set(k, expandVarsWith(v, overrides))
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxRespBytes))
	if err != nil {
		return nil, err
	}
	return &Response{Code: resp.StatusCode, Header: resp.Header, Body: body}, nil
}

func buildURL(base, endpoint string) string {
	if endpoint == "" {
		return base
	}
	return strings.TrimRight(base, "/") + "/" + strings.TrimLeft(endpoint, "/")
}
