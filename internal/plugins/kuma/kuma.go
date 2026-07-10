// Package kuma provides the /kuma command (admin): manage Uptime Kuma REST
// wrapper credentials and create/list/delete monitors, including bulk-adding
// monitors from the /cf domain record book.
package kuma

import (
	"context"
	"fmt"
	"strings"
	"time"

	tele "gopkg.in/telebot.v3"

	"github.com/moyoez/magibox/internal/auth"
	cf "github.com/moyoez/magibox/internal/cloudflare"
	kuma "github.com/moyoez/magibox/internal/kuma"
	"github.com/moyoez/magibox/pkg/plugin"
)

const usage = `Uptime Kuma 用法(/kuma <子命令>,需 admin):
凭据(REST 包装器地址 + 可选 API Key):
  /kuma cred add <名> <base_url> [api_key]
  /kuma cred list | del <名>
建监控(URL 取 https://<域名>):
  /kuma add <名> <域名>           针对指定域名
  /kuma add <名> all [大类]       全局:把 /cf 记录库里的域名批量建监控(可按大类,跳过被ban)
查询 / 删除:
  /kuma list <名>
  /kuma del <名> <monitor_id>

说明:对接 keithah/uptime-kuma-rest-api;API Key 仅在你给包装器加了鉴权时需要(以 Bearer 发送),不加就留空。`

type Plugin struct{ plugin.Base }

func (Plugin) Name() string { return "kuma" }

func (Plugin) Commands() []plugin.Command {
	return []plugin.Command{{
		Name:        "kuma",
		Description: "Uptime Kuma 监控管理(需 admin):/kuma help 看用法",
		Middleware:  []tele.MiddlewareFunc{auth.RequireAdmin()},
		Handler:     handle,
	}}
}

func handle(c tele.Context) error {
	args := strings.Fields(c.Message().Payload)
	if len(args) == 0 || args[0] == "help" {
		return c.Send(usage)
	}
	switch args[0] {
	case "cred":
		return handleCred(c, args)
	case "add":
		return handleAdd(c, args)
	case "list":
		return handleList(c, args)
	case "del":
		return handleDel(c, args)
	default:
		return c.Send("未知子命令:" + args[0] + "\n\n" + usage)
	}
}

func handleCred(c tele.Context, args []string) error {
	if len(args) < 2 {
		return c.Send("用法:/kuma cred add <名> <base_url> [api_key] | list | del <名>")
	}
	switch args[1] {
	case "add":
		if len(args) < 4 {
			return c.Send("用法:/kuma cred add <名> <base_url> [api_key]")
		}
		key := ""
		if len(args) >= 5 {
			key = args[4]
		}
		if err := kuma.AddCred(args[2], args[3], key); err != nil {
			return c.Send("失败:" + err.Error())
		}
		return c.Send("✅ 已添加 Kuma 凭据 " + args[2])
	case "list":
		creds := kuma.ListCreds()
		if len(creds) == 0 {
			return c.Send("(暂无凭据)。/kuma cred add <名> <base_url> [api_key]")
		}
		var sb strings.Builder
		sb.WriteString("Kuma 凭据:\n")
		for _, cr := range creds {
			key := "(无 key)"
			if cr.APIKey != "" {
				key = "key " + mask(cr.APIKey)
			}
			fmt.Fprintf(&sb, "• %s — %s,%s\n", cr.Name, cr.BaseURL, key)
		}
		return c.Send(sb.String())
	case "del":
		if len(args) < 3 {
			return c.Send("用法:/kuma cred del <名>")
		}
		if err := kuma.DelCred(args[2]); err != nil {
			return c.Send("失败:" + err.Error())
		}
		return c.Send("🗑 已删除 Kuma 凭据 " + args[2])
	default:
		return c.Send("未知操作 " + args[1] + "(add|list|del)")
	}
}

func handleAdd(c tele.Context, args []string) error {
	if len(args) < 3 {
		return c.Send("用法:/kuma add <名> <域名>  或  /kuma add <名> all [大类]")
	}
	cred, ok := kuma.GetCred(args[1])
	if !ok {
		return c.Send("没有这个凭据:" + args[1])
	}
	client := kuma.NewClient(cred)

	// Bulk mode: pull domains from the /cf record book (optionally by category),
	// skipping banned ones.
	if args[2] == "all" {
		category := ""
		if len(args) >= 4 {
			category = args[3]
		}
		var targets []string
		for _, d := range cf.ListDomains(category, "") {
			if d.Status == cf.StatusBanned {
				continue
			}
			targets = append(targets, d.Name)
		}
		if len(targets) == 0 {
			return c.Send("记录库里没有可监控的域名(全部被ban,或该大类为空)。先 /cf domain add")
		}
		var okList, fail []string
		for _, dom := range targets {
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			_, _, err := client.CreateMonitor(ctx, dom, monitorURL(dom))
			cancel()
			if err != nil {
				fail = append(fail, dom)
			} else {
				okList = append(okList, dom)
			}
		}
		scope := "全局"
		if category != "" {
			scope = "大类 " + category
		}
		msg := fmt.Sprintf("✅ %s:已建 %d 个监控", scope, len(okList))
		if len(fail) > 0 {
			msg += fmt.Sprintf("\n⚠️ 失败 %d 个:%s", len(fail), strings.Join(fail, ", "))
		}
		return c.Send(msg)
	}

	// Single domain.
	dom := args[2]
	if !cf.HostValid(dom) {
		return c.Send("域名格式不对:" + dom)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	id, kmsg, err := client.CreateMonitor(ctx, dom, monitorURL(dom))
	if err != nil {
		return c.Send("建监控失败:" + err.Error())
	}
	out := "✅ 已为 " + dom + " 建监控(" + monitorURL(dom) + ")"
	if id != 0 {
		out += fmt.Sprintf(",id=%d", id)
	}
	if kmsg != "" {
		out += "\n" + kmsg
	}
	return c.Send(out)
}

func handleList(c tele.Context, args []string) error {
	if len(args) < 2 {
		return c.Send("用法:/kuma list <名>")
	}
	cred, ok := kuma.GetCred(args[1])
	if !ok {
		return c.Send("没有这个凭据:" + args[1])
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	monitors, err := kuma.NewClient(cred).ListMonitors(ctx)
	if err != nil {
		return c.Send("拉取失败:" + err.Error())
	}
	if len(monitors) == 0 {
		return c.Send("(该 Kuma 没有监控)")
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "监控(%d):\n", len(monitors))
	for _, m := range monitors {
		fmt.Fprintf(&sb, "• #%d %s %s\n", m.ID, m.Name, m.URL)
	}
	return c.Send(clip(sb.String(), 4000))
}

func handleDel(c tele.Context, args []string) error {
	if len(args) < 3 {
		return c.Send("用法:/kuma del <名> <monitor_id>")
	}
	cred, ok := kuma.GetCred(args[1])
	if !ok {
		return c.Send("没有这个凭据:" + args[1])
	}
	var id int
	if _, err := fmt.Sscanf(args[2], "%d", &id); err != nil || id <= 0 {
		return c.Send("monitor_id 需为正整数")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	if err := kuma.NewClient(cred).DeleteMonitor(ctx, id); err != nil {
		return c.Send("删除失败:" + err.Error())
	}
	return c.Send(fmt.Sprintf("🗑 已删除监控 #%d", id))
}

func monitorURL(domain string) string { return "https://" + domain }

func mask(v string) string {
	r := []rune(v)
	if len(r) <= 8 {
		return "***"
	}
	return string(r[:4]) + "***" + string(r[len(r)-4:])
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…(已截断)"
}

func init() { plugin.Register(Plugin{}) }
