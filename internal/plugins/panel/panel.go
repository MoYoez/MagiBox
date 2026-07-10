// Package panel serves a small admin web panel for the Cloudflare (/cf),
// Uptime Kuma (/kuma) and uptime-callback (/uptime) features. It rides the
// shared HTTP server under /panel and is gated by a single login code
// (PANEL_CODE) that issues a signed session cookie. Disabled when PANEL_CODE
// is unset.
package panel

import (
	"crypto/subtle"
	_ "embed"
	"net/http"
	"time"

	tele "gopkg.in/telebot.v3"

	"github.com/moyoez/magibox/internal/config"
	"github.com/moyoez/magibox/pkg/plugin"
)

//go:embed assets/index.html
var indexHTML []byte

type Plugin struct{ plugin.Base }

func (Plugin) Name() string { return "panel" }

// HTTPRoutes implements plugin.HTTPer. When PANEL_CODE is unset the panel is
// disabled (no routes mounted).
func (Plugin) HTTPRoutes(_ *tele.Bot) []plugin.Route {
	code := config.PanelCode()
	if code == "" {
		return nil
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/panel/api/login", loginHandler(code))
	mux.HandleFunc("/panel/api/logout", logoutHandler())
	mux.HandleFunc("/panel/api/session", sessionHandler(code))
	mux.HandleFunc("/panel/api/state", guard(code, stateHandler))
	mux.HandleFunc("/panel/api/kuma/monitors", guard(code, kumaMonitorsHandler))
	mux.HandleFunc("/panel/api/cf/domain/add", guard(code, cfDomainAddHandler))
	mux.HandleFunc("/panel/api/cf/domain/set", guard(code, cfDomainSetHandler))
	mux.HandleFunc("/panel/api/cf/domain/del", guard(code, cfDomainDelHandler))
	mux.HandleFunc("/panel/api/cf/bind", guard(code, cfBindHandler))
	mux.HandleFunc("/panel/api/cf/unbind", guard(code, cfUnbindHandler))
	mux.HandleFunc("/panel/api/kuma/add", guard(code, kumaAddHandler))
	mux.HandleFunc("/panel/api/kuma/del", guard(code, kumaDelHandler))
	mux.HandleFunc("/panel/", pageHandler) // catch-all: serve the SPA shell
	return []plugin.Route{{Pattern: "/panel/", Handler: mux}}
}

// pageHandler serves the single-page shell for any non-API path under /panel.
func pageHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(indexHTML)
}

// loginHandler verifies the code (constant-time) and sets a session cookie.
func loginHandler(code string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var body struct {
			Code string `json:"code"`
		}
		if err := readJSON(r, &body); err != nil {
			writeErr(w, http.StatusBadRequest, "bad request")
			return
		}
		if subtle.ConstantTimeCompare([]byte(body.Code), []byte(code)) != 1 {
			writeErr(w, http.StatusUnauthorized, "授权码不对")
			return
		}
		setSessionCookie(w, r, issueSession(code, time.Now()), int(sessionTTL.Seconds()))
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

func logoutHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		setSessionCookie(w, r, "", -1)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

// sessionHandler reports whether the caller currently holds a valid session.
func sessionHandler(code string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"authed": authed(r, code)})
	}
}

// guard wraps a handler so it only runs for authenticated sessions.
func guard(code string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authed(r, code) {
			writeErr(w, http.StatusUnauthorized, "未登录")
			return
		}
		next(w, r)
	}
}

func init() { plugin.Register(Plugin{}) }
