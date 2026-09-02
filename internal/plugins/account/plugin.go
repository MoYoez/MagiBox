// Package account binds a Telegram user to a verified ERP OpenID Connect
// identity and maps only current-application magi:auth:* permissions to the
// corresponding local auth:* capabilities.
package account

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	tele "gopkg.in/telebot.v3"

	"github.com/moyoez/magibox/internal/config"
	"github.com/moyoez/magibox/pkg/plugin"
)

var current *service

var newOIDCClient = newERPIdentityClient

type Plugin struct{ plugin.Base }

func (Plugin) Name() string { return "account" }

// Init enables the OIDC client when an issuer is configured. An entirely empty
// configuration preserves existing deployments; partial configuration stops
// startup instead of silently running with broken authentication.
func Init(runtime config.OIDCConfig) error {
	current = nil
	if oidcConfigEmpty(runtime) {
		return nil
	}
	if err := validateOIDCConfig(runtime); err != nil {
		return err
	}
	tokenCipher, err := newTokenCipher(runtime.EncryptionKey)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	client, err := newOIDCClient(ctx, runtime)
	if err != nil {
		return err
	}
	current, err = newService(runtime, client, tokenCipher, time.Now())
	if err != nil {
		return err
	}
	log.Printf("[oidc] ERP 账户绑定已启用 issuer=%s callback=%s", runtime.Issuer, runtime.RedirectURL)
	return nil
}

func oidcConfigEmpty(runtime config.OIDCConfig) bool {
	return runtime.Issuer == "" && runtime.ClientID == "" && runtime.ClientSecret == "" && runtime.EncryptionKey == ""
}

func validateOIDCConfig(runtime config.OIDCConfig) error {
	missing := make([]string, 0, 4)
	for name, value := range map[string]string{
		"MAGI_OIDC_ISSUER":         runtime.Issuer,
		"MAGI_OIDC_CLIENT_ID":      runtime.ClientID,
		"MAGI_OIDC_CLIENT_SECRET":  runtime.ClientSecret,
		"MAGI_OIDC_ENCRYPTION_KEY": runtime.EncryptionKey,
	} {
		if value == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("oidc: incomplete configuration, missing %s", strings.Join(missing, ", "))
	}
	issuer, err := url.Parse(runtime.Issuer)
	if err != nil || issuer.Scheme != "https" || issuer.Host == "" || issuer.RawQuery != "" || issuer.Fragment != "" {
		return fmt.Errorf("oidc: MAGI_OIDC_ISSUER must be an exact HTTPS issuer URL")
	}
	callback, err := url.Parse(runtime.RedirectURL)
	if err != nil || callback.Scheme != "https" || callback.Host == "" ||
		callback.Path != "/auth/oidc/callback" || callback.RawQuery != "" || callback.Fragment != "" {
		return fmt.Errorf("oidc: callback must be an exact HTTPS /auth/oidc/callback URL")
	}
	if runtime.StorePath == "" {
		return fmt.Errorf("oidc: binding store path is empty")
	}
	seen := make(map[string]struct{}, len(runtime.Scopes))
	for _, scope := range runtime.Scopes {
		if scope == "" || strings.ContainsAny(scope, " \t\r\n") {
			return fmt.Errorf("oidc: invalid requested scope")
		}
		if _, exists := seen[scope]; exists {
			return fmt.Errorf("oidc: duplicate requested scope %q", scope)
		}
		seen[scope] = struct{}{}
	}
	for _, required := range []string{"openid", "offline_access"} {
		if _, exists := seen[required]; !exists {
			return fmt.Errorf("oidc: requested scopes must include %s", required)
		}
	}
	return nil
}

func (Plugin) Commands() []plugin.Command {
	return []plugin.Command{{
		Name:        "account",
		Description: "ERP 账户绑定与权限同步:/account bind|status|refresh|unbind",
		Handler:     handleAccount,
	}}
}

func (Plugin) Jobs() []plugin.Job {
	return []plugin.Job{{
		Name: "ERP OIDC 权限续期",
		Spec: "@every 1m",
		Run: func(_ *tele.Bot) {
			if current != nil {
				current.refreshDue()
			}
		},
	}}
}

func handleAccount(c tele.Context) error {
	if current == nil {
		return c.Send("ERP 账户绑定尚未配置")
	}
	if c.Chat() == nil || c.Chat().Type != tele.ChatPrivate || c.Sender() == nil || c.Sender().ID <= 0 {
		return c.Send("请在与 bot 的私聊中使用 /account，避免绑定链接或账户信息泄露")
	}
	fields := strings.Fields(c.Message().Payload)
	subcommand := "status"
	if len(fields) > 0 {
		subcommand = strings.ToLower(fields[0])
	}
	switch subcommand {
	case "bind":
		bindingURL, err := current.issueBindingURL(telegramAccountFromUser(c.Sender()))
		if err != nil {
			return c.Send("生成绑定链接失败，请稍后再试")
		}
		return c.Send("请在 10 分钟内打开下面的单次链接，在 ERP 登录并确认授权范围：\n" + bindingURL)
	case "status":
		item, ok := current.store.binding(c.Sender().ID)
		if !ok {
			return c.Send("尚未绑定 ERP 账户。使用 /account bind 开始绑定")
		}
		return c.Send(formatStatus(item, current.now()))
	case "refresh":
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		defer cancel()
		item, err := current.refreshNow(ctx, c.Sender().ID)
		if errors.Is(err, errAccountNotBound) {
			return c.Send("尚未绑定 ERP 账户。使用 /account bind 开始绑定")
		}
		if errors.Is(err, errCredentialRejected) || errors.Is(err, errAccessDenied) {
			return c.Send("ERP 当前未下发可用授权，ERP 来源权限已清空；账户绑定保留，系统会继续定期刷新")
		}
		if err != nil {
			log.Printf("[oidc] 手动刷新失败 telegram_user_id=%d: %v", c.Sender().ID, err)
			return c.Send("刷新失败；原有 ERP 权限仅保留到当前凭据到期，请稍后重试")
		}
		return c.Send("✅ 已重新读取 ERP 权限并原子替换\n当前 ERP 下发权限：" + formatPermissionList(item))
	case "unbind":
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		defer cancel()
		removed, err := current.unbind(ctx, c.Sender().ID)
		if !removed {
			return c.Send("当前没有 ERP 账户绑定")
		}
		if err != nil {
			log.Printf("[oidc] ERP 撤销失败 telegram_user_id=%d: %v", c.Sender().ID, err)
			return c.Send("✅ 本地账户绑定与权限已移除；ERP 凭据撤销暂时失败，可在 ERP「我的授权」中再次撤销")
		}
		return c.Send("✅ ERP 账户绑定、刷新凭据和 ERP 下发权限已移除")
	default:
		return c.Send("用法：/account bind | status | refresh | unbind")
	}
}

func formatStatus(item binding, now time.Time) string {
	identity := item.DisplayName
	if identity == "" {
		identity = item.Username
	}
	if identity == "" {
		identity = "已验证账户"
	}
	permissions := formatPermissionList(item)
	remaining := time.Unix(item.AccessExpiresAt, 0).Sub(now).Round(time.Second)
	if !now.Before(time.Unix(item.AccessExpiresAt, 0)) {
		remaining = 0
	}
	return fmt.Sprintf("ERP 账户：%s\n状态：已绑定，每 5 分钟同步\nERP 下发权限：%s\n当前访问凭据剩余：%s", identity, permissions, remaining)
}

func formatPermissionList(item binding) string {
	if len(item.Permissions) == 0 {
		return "（无）"
	}
	values := make([]string, len(item.Permissions))
	for index, permission := range item.Permissions {
		values[index] = permission.String()
	}
	return strings.Join(values, ", ")
}

func (Plugin) HTTPRoutes(_ *tele.Bot) []plugin.Route {
	mux := http.NewServeMux()
	mux.HandleFunc("/auth/oidc/start", startHandler)
	mux.HandleFunc("/auth/oidc/callback", callbackHandler)
	return []plugin.Route{{Pattern: "/auth/oidc/", Handler: mux}}
}

func startHandler(w http.ResponseWriter, r *http.Request) {
	secureHeaders(w)
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if current == nil {
		http.Error(w, "ERP 账户绑定尚未配置", http.StatusServiceUnavailable)
		return
	}
	ticket := r.URL.Query().Get("ticket")
	if r.URL.Query().Get("confirm") != "1" {
		account, err := current.previewTicket(ticket)
		if err != nil {
			writePage(w, http.StatusBadRequest, "绑定链接无效", "链接已使用或已过期，请回到 Telegram 重新执行 /account bind。")
			return
		}
		writeConfirmPage(w, ticket, account)
		return
	}
	redirect, state, err := current.begin(ticket)
	if err != nil {
		writePage(w, http.StatusBadRequest, "绑定链接无效", "链接已使用或已过期，请回到 Telegram 重新执行 /account bind。")
		return
	}
	setTransactionCookie(w, r, state, int(transactionTTL.Seconds()))
	http.Redirect(w, r, redirect, http.StatusFound)
}

func callbackHandler(w http.ResponseWriter, r *http.Request) {
	secureHeaders(w)
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if current == nil {
		http.Error(w, "ERP 账户绑定尚未配置", http.StatusServiceUnavailable)
		return
	}
	state := r.URL.Query().Get("state")
	cookie, cookieErr := r.Cookie(transactionCookieName)
	if cookieErr != nil || state == "" || !constantTimeStringEqual(cookie.Value, state) {
		writePage(w, http.StatusBadRequest, "登录状态无效", "请回到 Telegram 重新执行 /account bind。")
		return
	}
	if providerError := r.URL.Query().Get("error"); providerError != "" {
		_, _ = current.store.consumeTransaction(state, current.now())
		clearTransactionCookie(w, r)
		writePage(w, http.StatusUnauthorized, "ERP 未完成授权", "你可以关闭此页面，回到 Telegram 后重新发起绑定。")
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		writePage(w, http.StatusBadRequest, "缺少授权结果", "请回到 Telegram 重新执行 /account bind。")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	item, err := current.complete(ctx, state, code)
	clearTransactionCookie(w, r)
	if err != nil {
		message := "认证或权限校验失败，请回到 Telegram 重新发起绑定。"
		if errors.Is(err, errAccessDenied) {
			message = "ERP 当前未允许此账户访问 Magi，请联系应用管理员确认访问人员或组织组。"
		} else if errors.Is(err, errIdentityAlreadyBound) || errors.Is(err, errTelegramAlreadyBound) {
			message = "该 ERP 身份或 Telegram 账户已绑定其他账户，请先解绑后再试。"
		}
		log.Printf("[oidc] 回调校验失败: %v", err)
		writePage(w, http.StatusUnauthorized, "绑定失败", message)
		return
	}
	permissions := "未下发额外指令权限"
	if len(item.Permissions) > 0 {
		values := make([]string, len(item.Permissions))
		for index, permission := range item.Permissions {
			values[index] = permission.String()
		}
		permissions = "已下发：" + strings.Join(values, "、")
	}
	writePage(w, http.StatusOK, "ERP 账户绑定成功", permissions+"。现在可以关闭页面并回到 Telegram。")
}

const transactionCookieName = "mb_oidc_tx"

func setTransactionCookie(w http.ResponseWriter, _ *http.Request, state string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: transactionCookieName, Value: state, Path: "/auth/oidc/callback",
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode, MaxAge: maxAge,
	})
}

func clearTransactionCookie(w http.ResponseWriter, r *http.Request) {
	setTransactionCookie(w, r, "", -1)
}

func secureHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; frame-ancestors 'none'")
}

func telegramAccountFromUser(user *tele.User) telegramAccount {
	if user == nil {
		return telegramAccount{}
	}
	name := strings.TrimSpace(user.FirstName)
	if last := strings.TrimSpace(user.LastName); last != "" {
		if name == "" {
			name = last
		} else {
			name += " " + last
		}
	}
	return telegramAccount{ID: user.ID, Name: name, Username: user.Username}
}

func writePage(w http.ResponseWriter, status int, title, message string) {
	kind := "err"
	if status < 400 {
		kind = "ok"
	}
	writeHTML(w, status, kind, html.EscapeString(title), "<p>"+html.EscapeString(message)+"</p>", "", "")
}

func writeConfirmPage(w http.ResponseWriter, ticket string, account telegramAccount) {
	confirm := "/auth/oidc/start?ticket=" + url.QueryEscape(ticket) + "&confirm=1"
	var pad strings.Builder
	pad.WriteString(`<div class="pad"><div class="k">TELEGRAM</div>`)
	if account.Name != "" {
		pad.WriteString(`<div class="name">` + html.EscapeString(account.Name) + `</div>`)
	}
	var meta []string
	if account.Username != "" {
		meta = append(meta, "@"+account.Username)
	}
	if account.ID > 0 {
		meta = append(meta, strconv.FormatInt(account.ID, 10))
	}
	if len(meta) > 0 {
		pad.WriteString(`<div class="meta">` + html.EscapeString(strings.Join(meta, " · ")) + `</div>`)
	}
	pad.WriteString(`</div>`)
	body := "<p>即将把 ERP 账户绑定到这个 Telegram。</p>" + pad.String() +
		"<p>若这不是你本人，请直接关闭此页面，不要继续。</p>"
	writeHTML(w, http.StatusOK, "ask", "确认绑定 ERP 账户", body, html.EscapeString(confirm), "确认并前往 ERP 登录")
}

func writeHTML(w http.ResponseWriter, status int, kind, title, body, actionHref, actionLabel string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	action := ""
	if actionHref != "" && actionLabel != "" {
		action = `<p class="actions"><a class="action" href="` + actionHref + `">` + html.EscapeString(actionLabel) + `</a></p>`
	}
	_, _ = fmt.Fprintf(w, `<!doctype html>
<html lang="zh-CN">
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>%s</title>
<style>
:root{color-scheme:light}
*{box-sizing:border-box}
body{margin:0;min-height:100vh;background:#fff;color:#343a46;font-family:ui-sans-serif,system-ui,"PingFang SC","Noto Sans SC",sans-serif}
main{width:min(36rem,100%%);margin:0 auto;padding:56px 24px 80px}
.voice{margin:0 0 18px;font-size:15px;font-style:italic;color:#78808c}
h1{margin:0 0 16px;font-size:1.75rem;font-weight:650;letter-spacing:-.02em;line-height:1.25;color:#343a46}
p{margin:0 0 14px;font-size:15px;line-height:1.55;color:#4a5160}
.pad{display:inline-block;min-width:12rem;margin:4px 0 18px;padding:14px 16px;background:#f6f7f9;border-radius:18px}
.pad .k{font-family:ui-monospace,"SFMono-Regular",Menlo,monospace;font-size:11px;letter-spacing:.04em;color:#98a1ae}
.pad .name{margin-top:6px;font-size:18px;font-weight:650;color:#343a46}
.pad .meta{margin-top:4px;font-family:ui-monospace,"SFMono-Regular",Menlo,monospace;font-size:13px;color:#4a5160}
.actions{margin:28px 0 0}
.action{display:inline-flex;align-items:center;justify-content:center;min-height:44px;padding:0 18px;border-radius:18px;background:#f6f7f9;color:#343a46;text-decoration:none;font-weight:600}
</style>
<main class="%s">
<p class="voice">Mooding~</p>
<h1>%s</h1>
%s%s
</main>
</html>`, title, kind, title, body, action)
}

func init() { plugin.Register(Plugin{}) }
