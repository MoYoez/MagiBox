// Package panel serves a small admin web panel for the Cloudflare (/cf),
// Uptime Kuma (/kuma) and uptime-callback (/uptime) features. It rides the
// shared HTTP server under /panel.
//
// Access is by admin-generated one-time codes: an admin runs /panel new in the
// bot to mint a single-use code, which the operator enters at the login page to
// receive a signed session cookie valid for one month. Disable the panel by
// blacklisting the "panel" plugin.
package panel

import (
	_ "embed"
	"net/http"
	"strings"
	"time"

	tele "gopkg.in/telebot.v3"

	"github.com/moyoez/magibox/internal/auth"
	"github.com/moyoez/magibox/internal/config"
	"github.com/moyoez/magibox/pkg/plugin"
)

//go:embed assets/index.html
var indexHTML []byte

type Plugin struct{ plugin.Base }

func (Plugin) Name() string { return "panel" }

// Commands exposes /panel (admin) to mint and manage one-time login codes.
func (Plugin) Commands() []plugin.Command {
	return []plugin.Command{{
		Name:        "panel",
		Description: "后台面板:生成一次性登录码(需 admin):/panel new|list|revoke",
		Middleware:  []tele.MiddlewareFunc{auth.RequireAdmin()},
		Handler:     handlePanel,
	}}
}

func handlePanel(c tele.Context) error {
	args := strings.Fields(c.Message().Payload)
	sub := "new"
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "new":
		code, err := NewCode(time.Now().Unix())
		if err != nil {
			return c.Send("失败:" + err.Error())
		}
		url := strings.TrimRight(config.PublicBaseURL(), "/") + "/panel/"
		return c.Send("🔑 一次性登录码(用一次即失效,登录后有效期 30 天):\n" + code + "\n\n面板地址:" + url)
	case "list":
		codes := ListCodes()
		if len(codes) == 0 {
			return c.Send("(没有待用的登录码)。/panel new 生成一个")
		}
		return c.Send("待用登录码(各仅一次):\n" + strings.Join(codes, "\n"))
	case "revoke":
		if len(args) < 2 {
			return c.Send("用法:/panel revoke <码>")
		}
		if Revoke(args[1]) {
			return c.Send("🗑 已撤销 " + args[1])
		}
		return c.Send("没有这个待用码:" + args[1])
	default:
		return c.Send("用法:/panel new | list | revoke <码>")
	}
}

// HTTPRoutes implements plugin.HTTPer, mounting the panel under /panel. It is
// only called for the enabled plugin, so blacklisting "panel" disables it.
func (Plugin) HTTPRoutes(_ *tele.Bot) []plugin.Route {
	mux := http.NewServeMux()
	mux.HandleFunc("/panel/api/login", loginHandler)
	mux.HandleFunc("/panel/api/logout", logoutHandler)
	mux.HandleFunc("/panel/api/session", sessionHandler)
	mux.HandleFunc("/panel/api/state", guard(stateHandler))
	mux.HandleFunc("/panel/api/kuma/monitors", guard(kumaMonitorsHandler))
	mux.HandleFunc("/panel/api/cf/domain/add", guard(cfDomainAddHandler))
	mux.HandleFunc("/panel/api/cf/domain/set", guard(cfDomainSetHandler))
	mux.HandleFunc("/panel/api/cf/domain/del", guard(cfDomainDelHandler))
	mux.HandleFunc("/panel/api/cf/bind", guard(cfBindHandler))
	mux.HandleFunc("/panel/api/cf/unbind", guard(cfUnbindHandler))
	mux.HandleFunc("/panel/api/cf/worker/import", guard(cfWorkerImportHandler))
	mux.HandleFunc("/panel/api/cf/failover/add", guard(cfFailoverAddHandler))
	mux.HandleFunc("/panel/api/cf/failover/del", guard(cfFailoverDelHandler))
	mux.HandleFunc("/panel/api/cf/failover/target", guard(cfFailoverTargetHandler))
	mux.HandleFunc("/panel/api/cf/failover/mode", guard(cfFailoverModeHandler))
	mux.HandleFunc("/panel/api/cf/failover/apply", guard(cfFailoverApplyHandler))
	mux.HandleFunc("/panel/api/kuma/add", guard(kumaAddHandler))
	mux.HandleFunc("/panel/api/kuma/del", guard(kumaDelHandler))
	mux.HandleFunc("/panel/", pageHandler) // catch-all: serve the SPA shell
	return []plugin.Route{{Pattern: "/panel/", Handler: mux}}
}

// pageHandler serves the single-page shell for any non-API path under /panel.
func pageHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(indexHTML)
}

// loginHandler consumes a one-time code and, on success, sets a session cookie.
func loginHandler(w http.ResponseWriter, r *http.Request) {
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
	if !ConsumeCode(strings.TrimSpace(body.Code)) {
		writeErr(w, http.StatusUnauthorized, "授权码无效或已被使用")
		return
	}
	setSessionCookie(w, r, issueSession(time.Now()), int(sessionTTL.Seconds()))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func logoutHandler(w http.ResponseWriter, r *http.Request) {
	setSessionCookie(w, r, "", -1)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// sessionHandler reports whether the caller currently holds a valid session.
func sessionHandler(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"authed": authed(r)})
}

// guard wraps a handler so it only runs for authenticated sessions.
func guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authed(r) {
			writeErr(w, http.StatusUnauthorized, "未登录")
			return
		}
		next(w, r)
	}
}

func init() { plugin.Register(Plugin{}) }
