// Package uptime provides the /uptime command: register inbound webhook
// watchers (e.g. for Uptime Kuma), each exposing an unguessable callback URL
// that formats the received JSON and pushes it to a target chat.
package uptime

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"

	tele "gopkg.in/telebot.v3"

	"github.com/moyoez/magibox/internal/auth"
	"github.com/moyoez/magibox/internal/config"
	up "github.com/moyoez/magibox/internal/uptime"
	"github.com/moyoez/magibox/pkg/plugin"
)

const (
	hookPrefix   = "/hook/uptime/"
	maxBodyBytes = 1 << 20 // read limit per inbound webhook
	maxMsgRunes  = 4000     // Telegram message length ceiling
)

const usage = `uptime 用法(/uptime <子命令>,需 admin):
  new <名字>                新建监听,返回专属回调地址
  list                      列出所有监听
  show <名字>               查看配置(含回调地址)
  url <名字>                只打印回调地址
  del <名字>                删除监听
  target <名字> <chat_id|here>  设置通知目标(here=当前会话;群 id 用 /whoami 拿)
  fields <名字> <a,b,c>     设置要提取的 JSON 字段(逗号分隔,支持 a.b.c 点路径)
  template <名字> <文案>    自定义文案,用 {a.b} 占位;template <名字> clear 清除
  test <名字> [JSON]        用示例或给定 JSON 演练一次推送

回调:把 url 打印出的地址填到 Uptime Kuma 的「Webhook」通知里即可。
文案:未设 template 时,fields 里每个字段渲染成「字段: 值」一行;
      "ipgroup,isbanned" 会解析回调 JSON 的 ipgroup / isbanned 字段。`

type Plugin struct{ plugin.Base }

func (Plugin) Name() string { return "uptime" }

func (Plugin) Commands() []plugin.Command {
	return []plugin.Command{{
		Name:        "uptime",
		Description: "Uptime Kuma 回调监听(需 admin):/uptime help 看用法",
		Middleware:  []tele.MiddlewareFunc{auth.RequireAdmin()},
		Handler:     handle,
	}}
}

// HTTPRoutes implements plugin.HTTPer: mount the inbound webhook receiver on
// the shared server.
func (Plugin) HTTPRoutes(b *tele.Bot) []plugin.Route {
	return []plugin.Route{{
		Pattern: hookPrefix,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			handleHook(b, w, r)
		}),
	}}
}

// handleHook receives an Uptime Kuma (or any JSON) webhook, formats it per the
// watcher's spec, and pushes it to the target chat. It answers 200 quickly so
// the caller does not retry.
func handleHook(b *tele.Bot, w http.ResponseWriter, r *http.Request) {
	token := strings.Trim(strings.TrimPrefix(r.URL.Path, hookPrefix), "/")
	watcher, ok := up.ByToken(token)
	if token == "" || !ok {
		http.NotFound(w, r)
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, "ok")

	if watcher.Target == 0 {
		log.Printf("[uptime] 收到 %s 回调,但未设置通知目标(/uptime target %s here),已忽略", watcher.Name, watcher.Name)
		return
	}
	text := clip(up.Render(watcher, body), maxMsgRunes)
	if _, err := b.Send(tele.ChatID(watcher.Target), text); err != nil {
		log.Printf("[uptime] 推送 %s 到 %d 失败: %v", watcher.Name, watcher.Target, err)
	}
}

func handle(c tele.Context) error {
	args := strings.Fields(c.Message().Payload)
	if len(args) == 0 || args[0] == "help" {
		return c.Send(usage)
	}
	switch args[0] {
	case "new":
		if len(args) < 2 {
			return c.Send("用法:/uptime new <名字>")
		}
		w, err := up.Create(args[1])
		if err != nil {
			return c.Send("失败:" + err.Error())
		}
		return c.Send("✅ 已创建监听 " + w.Name + "\n回调地址(填到 Uptime Kuma 的 Webhook 通知):\n" + callbackURL(w.Token) +
			"\n\n下一步:/uptime target " + w.Name + " here 绑定通知目标")

	case "list":
		ws := up.List()
		if len(ws) == 0 {
			return c.Send("(暂无监听)。/uptime new <名字> 新建")
		}
		var sb strings.Builder
		sb.WriteString("监听:\n")
		for _, w := range ws {
			fmt.Fprintf(&sb, "• %s — 目标 %s%s\n", w.Name, targetText(w.Target), fieldsMark(w))
		}
		return c.Send(sb.String())

	case "show":
		if len(args) < 2 {
			return c.Send("用法:/uptime show <名字>")
		}
		w, ok := up.Get(args[1])
		if !ok {
			return c.Send("没有这个监听:" + args[1])
		}
		return c.Send(showWatcher(w))

	case "url":
		if len(args) < 2 {
			return c.Send("用法:/uptime url <名字>")
		}
		w, ok := up.Get(args[1])
		if !ok {
			return c.Send("没有这个监听:" + args[1])
		}
		return c.Send(callbackURL(w.Token))

	case "del":
		if len(args) < 2 {
			return c.Send("用法:/uptime del <名字>")
		}
		if err := up.Delete(args[1]); err != nil {
			return c.Send("失败:" + err.Error())
		}
		return c.Send("🗑 已删除 " + args[1])

	case "target":
		return handleTarget(c, args)

	case "fields":
		if len(args) < 3 {
			return c.Send("用法:/uptime fields <名字> <a,b,c>\n例:/uptime fields demo ipgroup,isbanned")
		}
		spec := strings.Join(args[2:], "")
		fields := up.ParseFields(spec)
		if err := up.Mutate(args[1], func(w *up.Watcher) error {
			w.Fields = fields
			w.Template = "" // fields and template are mutually exclusive
			return nil
		}); err != nil {
			return c.Send("失败:" + err.Error())
		}
		return c.Send(fmt.Sprintf("✅ %s 的字段已设为:%s", args[1], strings.Join(fields, ", ")))

	case "template":
		return handleTemplate(c, args)

	case "test":
		return handleTest(c, args)

	default:
		return c.Send("未知子命令:" + args[0] + "\n\n" + usage)
	}
}

func handleTarget(c tele.Context, args []string) error {
	if len(args) < 3 {
		return c.Send("用法:/uptime target <名字> <chat_id|here>")
	}
	var target int64
	if args[2] == "here" {
		target = c.Chat().ID
	} else {
		id, err := strconv.ParseInt(args[2], 10, 64)
		if err != nil {
			return c.Send("chat_id 需为整数,或用 here 表示当前会话")
		}
		target = id
	}
	if err := up.Mutate(args[1], func(w *up.Watcher) error {
		w.Target = target
		return nil
	}); err != nil {
		return c.Send("失败:" + err.Error())
	}
	return c.Send(fmt.Sprintf("✅ %s 的通知目标已设为 %d", args[1], target))
}

func handleTemplate(c tele.Context, args []string) error {
	if len(args) < 3 {
		return c.Send("用法:/uptime template <名字> <文案,用 {a.b} 占位>\n清除:/uptime template <名字> clear")
	}
	tmpl := rest(c.Message().Payload, 2)
	if strings.TrimSpace(tmpl) == "clear" {
		tmpl = ""
	}
	if err := up.Mutate(args[1], func(w *up.Watcher) error {
		w.Template = tmpl
		return nil
	}); err != nil {
		return c.Send("失败:" + err.Error())
	}
	if tmpl == "" {
		return c.Send("✅ 已清除 " + args[1] + " 的自定义文案")
	}
	return c.Send("✅ 已设置 " + args[1] + " 的自定义文案")
}

func handleTest(c tele.Context, args []string) error {
	if len(args) < 2 {
		return c.Send("用法:/uptime test <名字> [JSON]")
	}
	w, ok := up.Get(args[1])
	if !ok {
		return c.Send("没有这个监听:" + args[1])
	}
	body := []byte(sampleBody)
	if raw := strings.TrimSpace(rest(c.Message().Payload, 2)); raw != "" {
		body = []byte(raw)
	}
	text := clip(up.Render(w, body), maxMsgRunes)
	if w.Target == 0 {
		return c.Send("(未设通知目标,以下为渲染预览)\n\n" + text)
	}
	if _, err := c.Bot().Send(tele.ChatID(w.Target), text); err != nil {
		return c.Send("推送失败:" + err.Error())
	}
	return c.Send("✅ 已把演练消息推给目标 " + strconv.FormatInt(w.Target, 10))
}

const sampleBody = `{"heartbeat":{"status":0,"msg":"connection failed"},"monitor":{"name":"demo"},"msg":"[demo] Down","ipgroup":"cn-east","isbanned":true}`

func callbackURL(token string) string {
	return strings.TrimRight(config.PublicBaseURL(), "/") + hookPrefix + token
}

func targetText(target int64) string {
	if target == 0 {
		return "(未设)"
	}
	return strconv.FormatInt(target, 10)
}

func fieldsMark(w *up.Watcher) string {
	if w.Template != "" {
		return " 📝自定义文案"
	}
	if len(w.Fields) > 0 {
		return " [" + strings.Join(w.Fields, ",") + "]"
	}
	return ""
}

func showWatcher(w *up.Watcher) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "监听:%s\n回调地址:%s\n通知目标:%s\n", w.Name, callbackURL(w.Token), targetText(w.Target))
	if w.Template != "" {
		fmt.Fprintf(&sb, "自定义文案:\n%s\n", w.Template)
	} else if len(w.Fields) > 0 {
		fmt.Fprintf(&sb, "提取字段:%s\n", strings.Join(w.Fields, ", "))
	} else {
		sb.WriteString("文案:默认(取 msg 字段或原文)\n")
	}
	return sb.String()
}

// rest skips the first skip whitespace-separated tokens and returns the raw
// remainder (spaces preserved), for free-form template text.
func rest(s string, skip int) string {
	s = strings.TrimLeft(s, " \t\n")
	for i := 0; i < skip; i++ {
		idx := strings.IndexAny(s, " \t\n")
		if idx < 0 {
			return ""
		}
		s = strings.TrimLeft(s[idx:], " \t\n")
	}
	return s
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…(已截断)"
}

func init() { plugin.Register(Plugin{}) }
