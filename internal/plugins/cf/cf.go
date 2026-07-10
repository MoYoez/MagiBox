// Package cf provides the /cf command (admin): manage Cloudflare credentials,
// Workers, a domain record book, and attach/detach Worker Custom Domains.
package cf

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tele "gopkg.in/telebot.v3"

	"github.com/moyoez/magibox/internal/auth"
	cf "github.com/moyoez/magibox/internal/cloudflare"
	"github.com/moyoez/magibox/pkg/plugin"
)

const usage = `Cloudflare 用法(/cf <子命令>,需 admin):
凭据(多凭据):
  /cf cred add <名> <account_id> <api_token>
  /cf cred list | del <名>
Worker(绑定凭据 + 大类):
  /cf worker add <worker> <凭据> [大类]
  /cf worker cat <worker> <大类>          改绑大类
  /cf worker list | del <worker>
记录库(大类 / 小类 / 状态 + 生命周期字段):
  /cf domain add <大类> <域名[,域名...]> [小类]
  /cf domain list [大类] [状态]
  /cf domain status <域名> <未使用|已使用|ban>
  /cf domain set <域名> <字段> <值>
      分类:category 大类 | sub 小类
      生命周期:purchased 购买日期 | usage 用途 | dns DNS | ready 就绪(yes/no) | changed 更换时间
  /cf domain del <域名>
绑定(Custom Domains,自动挑域名 + 自动改状态):
  /cf bind <worker> [域名] [force]
      不给域名 → 从该 worker 绑定的大类里取一个「未使用」域名
      域名已绑到别的 worker → 提示,加 force 强制替换
  /cf unbind <域名>       解绑,域名回到「未使用」
  /cf show <worker|域名>  查看当前记录`

type Plugin struct{ plugin.Base }

func (Plugin) Name() string { return "cf" }

func (Plugin) Commands() []plugin.Command {
	return []plugin.Command{{
		Name:        "cf",
		Description: "Cloudflare Worker 域名管理(需 admin):/cf help 看用法",
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
	case "worker":
		return handleWorker(c, args)
	case "domain":
		return handleDomain(c, args)
	case "bind":
		return handleBind(c, args)
	case "unbind":
		return handleUnbind(c, args)
	case "show":
		return handleShow(c, args)
	default:
		return c.Send("未知子命令:" + args[0] + "\n\n" + usage)
	}
}

// --- creds ---

func handleCred(c tele.Context, args []string) error {
	if len(args) < 2 {
		return c.Send("用法:/cf cred add <名> <account_id> <api_token> | list | del <名>")
	}
	switch args[1] {
	case "add":
		if len(args) < 5 {
			return c.Send("用法:/cf cred add <名> <account_id> <api_token>")
		}
		if err := cf.AddCred(args[2], args[3], args[4]); err != nil {
			return c.Send("失败:" + err.Error())
		}
		return c.Send("✅ 已添加凭据 " + args[2])
	case "list":
		creds := cf.ListCreds()
		if len(creds) == 0 {
			return c.Send("(暂无凭据)。/cf cred add <名> <account_id> <token>")
		}
		var sb strings.Builder
		sb.WriteString("凭据:\n")
		for _, cr := range creds {
			fmt.Fprintf(&sb, "• %s — account %s,token %s\n", cr.Name, cr.AccountID, mask(cr.Token))
		}
		return c.Send(sb.String())
	case "del":
		if len(args) < 3 {
			return c.Send("用法:/cf cred del <名>")
		}
		if err := cf.DelCred(args[2]); err != nil {
			return c.Send("失败:" + err.Error())
		}
		return c.Send("🗑 已删除凭据 " + args[2])
	default:
		return c.Send("未知操作 " + args[1] + "(add|list|del)")
	}
}

// --- workers ---

func handleWorker(c tele.Context, args []string) error {
	if len(args) < 2 {
		return c.Send("用法:/cf worker add <worker> <凭据> [大类] | cat <worker> <大类> | list | del <worker>")
	}
	switch args[1] {
	case "add":
		if len(args) < 4 {
			return c.Send("用法:/cf worker add <worker> <凭据> [大类]")
		}
		category := ""
		if len(args) >= 5 {
			category = args[4]
		}
		if err := cf.AddWorker(args[2], args[3], category); err != nil {
			return c.Send("失败:" + err.Error())
		}
		msg := "✅ 已登记 worker " + args[2] + "(凭据 " + args[3] + ")"
		if category != "" {
			msg += ",大类 " + category
		}
		return c.Send(msg)
	case "cat":
		if len(args) < 4 {
			return c.Send("用法:/cf worker cat <worker> <大类>")
		}
		if !cf.NameValid(args[3]) {
			return c.Send("大类名只能是 a-zA-Z0-9_.-")
		}
		if err := cf.MutateWorker(args[2], func(w *cf.Worker) error { w.Category = args[3]; return nil }); err != nil {
			return c.Send("失败:" + err.Error())
		}
		return c.Send("✅ " + args[2] + " 的大类已设为 " + args[3])
	case "list":
		ws := cf.ListWorkers()
		if len(ws) == 0 {
			return c.Send("(暂无 worker)。/cf worker add <worker> <凭据> [大类]")
		}
		var sb strings.Builder
		sb.WriteString("Worker:\n")
		for _, w := range ws {
			cat := w.Category
			if cat == "" {
				cat = "(未绑大类)"
			}
			fmt.Fprintf(&sb, "• %s — 凭据 %s,大类 %s\n", w.Name, w.Cred, cat)
		}
		return c.Send(sb.String())
	case "del":
		if len(args) < 3 {
			return c.Send("用法:/cf worker del <worker>")
		}
		if err := cf.DelWorker(args[2]); err != nil {
			return c.Send("失败:" + err.Error())
		}
		return c.Send("🗑 已删除 worker 记录 " + args[2])
	default:
		return c.Send("未知操作 " + args[1] + "(add|cat|list|del)")
	}
}

// --- domains (record book) ---

func handleDomain(c tele.Context, args []string) error {
	if len(args) < 2 {
		return c.Send("用法:/cf domain add <大类> <域名[,...]> [小类] | list [大类] [状态] | status <域名> <状态> | del <域名>")
	}
	switch args[1] {
	case "add":
		if len(args) < 4 {
			return c.Send("用法:/cf domain add <大类> <域名[,域名...]> [小类]")
		}
		category := args[2]
		sub := ""
		if len(args) >= 5 {
			sub = args[4]
		}
		var ok, fail []string
		for _, d := range strings.Split(args[3], ",") {
			d = strings.TrimSpace(d)
			if d == "" {
				continue
			}
			if err := cf.AddDomain(category, d, sub); err != nil {
				fail = append(fail, d+"("+err.Error()+")")
			} else {
				ok = append(ok, d)
			}
		}
		var sb strings.Builder
		if len(ok) > 0 {
			fmt.Fprintf(&sb, "✅ 已加入大类 %s:%s\n", category, strings.Join(ok, ", "))
		}
		if len(fail) > 0 {
			fmt.Fprintf(&sb, "⚠️ 跳过:%s", strings.Join(fail, "; "))
		}
		return c.Send(strings.TrimSpace(sb.String()))
	case "list":
		category := ""
		if len(args) >= 3 {
			category = args[2]
		}
		status := ""
		if len(args) >= 4 {
			s, ok := cf.NormalizeStatus(args[3])
			if !ok {
				return c.Send("状态只能是 未使用|已使用|ban")
			}
			status = s
		}
		ds := cf.ListDomains(category, status)
		if len(ds) == 0 {
			return c.Send("(没有匹配的域名记录)")
		}
		var sb strings.Builder
		sb.WriteString("域名记录:\n")
		for _, d := range ds {
			line := fmt.Sprintf("• %s [%s] %s", d.Name, d.Category, statusText(d.Status))
			if d.Sub != "" {
				line += " /" + d.Sub
			}
			if d.Ready {
				line += " ✅就绪"
			}
			if d.Worker != "" {
				line += " → " + d.Worker
			}
			sb.WriteString(line + "\n")
		}
		return c.Send(sb.String())
	case "status":
		if len(args) < 4 {
			return c.Send("用法:/cf domain status <域名> <未使用|已使用|ban>")
		}
		if err := cf.SetDomainField(args[2], "status", args[3]); err != nil {
			return c.Send("失败:" + err.Error())
		}
		s, _ := cf.NormalizeStatus(args[3])
		return c.Send("✅ " + args[2] + " 状态已设为 " + statusText(s))
	case "set":
		return handleDomainSet(c, args)
	case "del":
		if len(args) < 3 {
			return c.Send("用法:/cf domain del <域名>")
		}
		if err := cf.DelDomain(args[2]); err != nil {
			return c.Send("失败:" + err.Error())
		}
		return c.Send("🗑 已删除域名记录 " + args[2])
	default:
		return c.Send("未知操作 " + args[1] + "(add|list|status|set|del)")
	}
}

// handleDomainSet sets one lifecycle field on a domain record. Value is the
// rest of the args joined, so free-text fields may contain spaces.
func handleDomainSet(c tele.Context, args []string) error {
	if len(args) < 5 {
		return c.Send("用法:/cf domain set <域名> <字段> <值>\n字段:category|sub|purchased|usage|dns|ready|changed")
	}
	domain, field := args[2], strings.ToLower(args[3])
	value := strings.Join(args[4:], " ")
	if err := cf.SetDomainField(domain, field, value); err != nil {
		return c.Send("失败:" + err.Error())
	}
	return c.Send(fmt.Sprintf("✅ %s 的 %s 已更新", domain, field))
}

// --- bind / unbind (Cloudflare Custom Domains) ---

func handleBind(c tele.Context, args []string) error {
	if len(args) < 2 {
		return c.Send("用法:/cf bind <worker> [域名] [force]")
	}
	workerName := args[1]
	// Parse optional [域名] and the force flag (order-independent for the flag).
	domain, force := "", false
	for _, a := range args[2:] {
		if a == "force" {
			force = true
		} else if domain == "" {
			domain = a
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	res, err := cf.BindDomain(ctx, workerName, domain, force)
	if err != nil {
		var conf *cf.ConflictError
		if errors.As(err, &conf) {
			return c.Send(fmt.Sprintf("⚠️ %s 已绑定到 worker「%s」。确认替换请加 force:\n/cf bind %s %s force",
				conf.Domain, conf.CurrentWorker, workerName, conf.Domain))
		}
		return c.Send("失败:" + err.Error())
	}
	msg := fmt.Sprintf("✅ 已把 %s 绑定到 worker「%s」(zone %s),并标记为「已使用」", res.Domain, res.Worker, res.ZoneName)
	if res.AutoPicked {
		msg = "🎯 自动选用域名 " + res.Domain + "\n" + msg
	}
	return c.Send(msg)
}

func handleUnbind(c tele.Context, args []string) error {
	if len(args) < 2 {
		return c.Send("用法:/cf unbind <域名>")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	if err := cf.UnbindDomain(ctx, args[1]); err != nil {
		return c.Send("失败:" + err.Error())
	}
	return c.Send("✅ 已解绑 " + args[1] + ",状态回到「未使用」")
}

func handleShow(c tele.Context, args []string) error {
	if len(args) < 2 {
		return c.Send("用法:/cf show <worker|域名>")
	}
	name := args[1]
	if w, ok := cf.GetWorker(name); ok {
		var sb strings.Builder
		cat := w.Category
		if cat == "" {
			cat = "(未绑大类)"
		}
		fmt.Fprintf(&sb, "Worker:%s\n凭据:%s\n大类:%s\n环境:%s\n", w.Name, w.Cred, cat, w.Env)
		attached := cf.ListDomains(w.Category, "")
		var mine []string
		for _, d := range attached {
			if d.Worker == w.Name {
				mine = append(mine, d.Name)
			}
		}
		if len(mine) > 0 {
			fmt.Fprintf(&sb, "已绑域名:%s\n", strings.Join(mine, ", "))
		}
		if w.Category != "" {
			if free := len(cf.ListDomains(w.Category, cf.StatusUnused)); free > 0 {
				fmt.Fprintf(&sb, "大类里还有 %d 个「未使用」域名\n", free)
			}
		}
		return c.Send(strings.TrimSpace(sb.String()))
	}
	if d, ok := cf.GetDomain(name); ok {
		var sb strings.Builder
		fmt.Fprintf(&sb, "域名:%s\n大类:%s\n状态:%s\n就绪:%s\n", d.Name, d.Category, statusText(d.Status), readyText(d.Ready))
		if d.Sub != "" {
			fmt.Fprintf(&sb, "小类:%s\n", d.Sub)
		}
		if d.Worker != "" {
			fmt.Fprintf(&sb, "绑定 worker:%s\n", d.Worker)
		}
		if d.PurchasedAt != "" {
			fmt.Fprintf(&sb, "购买日期:%s\n", d.PurchasedAt)
		}
		if d.Usage != "" {
			fmt.Fprintf(&sb, "用途:%s\n", d.Usage)
		}
		if d.DNS != "" {
			fmt.Fprintf(&sb, "DNS:%s\n", d.DNS)
		}
		if d.ChangedAt != "" {
			fmt.Fprintf(&sb, "更换时间:%s\n", d.ChangedAt)
		}
		return c.Send(strings.TrimSpace(sb.String()))
	}
	return c.Send("既不是已知 worker 也不是域名记录:" + name)
}

// --- helpers ---

func readyText(ready bool) string {
	if ready {
		return "✅ 就绪"
	}
	return "⬜ 未就绪"
}

func statusText(s string) string {
	switch s {
	case cf.StatusUnused:
		return "未使用"
	case cf.StatusUsed:
		return "已使用"
	case cf.StatusBanned:
		return "被ban"
	default:
		return s
	}
}

func mask(v string) string {
	r := []rune(v)
	if len(r) <= 8 {
		return "***"
	}
	return string(r[:4]) + "***" + string(r[len(r)-4:])
}

func init() { plugin.Register(Plugin{}) }
