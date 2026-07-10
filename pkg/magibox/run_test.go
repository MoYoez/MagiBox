package magibox

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/moyoez/magibox/pkg/plugin"
)

func TestRunRequiresBotToken(t *testing.T) {
	t.Setenv("BOT_TOKEN", "")
	err := Run()
	if err == nil {
		t.Fatal("Run() error = nil, want missing BOT_TOKEN error")
	}
	if !strings.Contains(err.Error(), "BOT_TOKEN") {
		t.Fatalf("Run() error = %q, want BOT_TOKEN context", err)
	}
}

func TestRunReturnsBundleListenError(t *testing.T) {
	serveTelegramAPI(t)

	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()

	pluginName := uniquePluginName("host-listen-error-test")
	plugin.Register(setupFailurePlugin{name: pluginName})
	configureRun(t, occupied.Addr().String(), pluginName)

	err = Run()
	if err == nil {
		t.Fatal("Run() error = nil, want bundle listen error")
	}
	if !strings.Contains(err.Error(), "bundle HTTP") {
		t.Fatalf("Run() error = %q, want bundle HTTP listen context", err)
	}
}

func TestRunClosesBundleListenerWhenSetupFails(t *testing.T) {
	serveTelegramAPI(t)

	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := probe.Addr().String()
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}

	listeningObserved := false
	pluginName := uniquePluginName("host-listener-cleanup-test")
	plugin.Register(setupFailurePlugin{
		name: pluginName,
		beforeJobs: func() {
			listeningObserved = waitForTCP(addr, 2*time.Second)
		},
	})
	configureRun(t, addr, pluginName)

	err = Run()
	if err == nil || !strings.Contains(err.Error(), "设置插件") {
		t.Fatalf("Run() error = %v, want plugin setup error", err)
	}
	if !listeningObserved {
		t.Fatal("bundle listener was not accepting connections before setup failed")
	}

	rebound, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("bundle listener remained open after Run returned: %v", err)
	}
	rebound.Close()
}

var pluginNameSequence atomic.Uint64

func uniquePluginName(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, pluginNameSequence.Add(1))
}

type setupFailurePlugin struct {
	plugin.Base
	name       string
	beforeJobs func()
}

func (p setupFailurePlugin) Name() string { return p.name }

func (p setupFailurePlugin) Jobs() []plugin.Job {
	if p.beforeJobs != nil {
		p.beforeJobs()
	}
	return []plugin.Job{{Name: "invalid", Spec: "not-a-cron-spec"}}
}

func configureRun(t *testing.T, addr, pluginName string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("BOT_TOKEN", "test-token")
	t.Setenv("BOT_NAME", "")
	t.Setenv("BOT_DESCRIPTION", "")
	t.Setenv("BOT_ABOUT", "")
	t.Setenv("AUTH_STORE", filepath.Join(dir, "auth.json"))
	t.Setenv("PLAYGROUND_STORE", filepath.Join(dir, "playground.json"))
	t.Setenv("VARS_STORE", filepath.Join(dir, "vars.json"))
	t.Setenv("BUNDLE_STORE", filepath.Join(dir, "bundles.json"))
	t.Setenv("BUNDLE_MEDIA_DIR", filepath.Join(dir, "media"))
	t.Setenv("BUNDLE_ADDR", addr)
	t.Setenv("BUNDLE_BASE_URL", "http://"+addr)
	t.Setenv("PLUGINS_MODE", plugin.ModeWhitelist)
	t.Setenv("PLUGINS_LIST", pluginName)
}

func serveTelegramAPI(t *testing.T) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"Test","username":"test_bot"}}`)
	}))

	target, err := url.Parse(server.URL)
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	previous := http.DefaultTransport
	http.DefaultTransport = redirectTransport{
		base:   server.Client().Transport,
		target: target,
	}
	t.Cleanup(func() {
		http.DefaultTransport = previous
		server.Close()
	})
}

type redirectTransport struct {
	base   http.RoundTripper
	target *url.URL
}

func (t redirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	redirected := req.Clone(req.Context())
	redirectedURL := *req.URL
	redirectedURL.Scheme = t.target.Scheme
	redirectedURL.Host = t.target.Host
	redirected.URL = &redirectedURL
	redirected.Host = t.target.Host
	return t.base.RoundTrip(redirected)
}

func waitForTCP(addr string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 20*time.Millisecond)
		if err == nil {
			conn.Close()
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}
