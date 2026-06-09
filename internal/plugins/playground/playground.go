// Package playground provides the /pg command: manage and execute HTTP API
// groups (admin required), with cron-based scheduled health checks plus
// assertion alerts (including debouncing and recovery notifications).
package playground

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
	tele "gopkg.in/telebot.v3"

	"github.com/moyoez/magibox/internal/auth"
	pg "github.com/moyoez/magibox/internal/playground"
	"github.com/moyoez/magibox/internal/plugin"
)

const usage = `playground 用法(/pg <子命令>):
  new <组>                       新建分组
  list                           列出所有分组
  show <组>                      查看分组配置
  del <组>                       删除分组
  set <组> baseurl|endpoint|method|body|template|threshold <值>
  header <组> set <键> <值> | del <键>
  run <组> [键=值 ...]           立即执行(可带运行时传参)
  sched <组> <cron> [键=值 ...] | off   定时巡检(配合断言;可带传参)
  assert <组> add <表达式> <==|!=|has> <值>
  assert <组> list | clear
  var set <键> <值> | del <键> | list

模板占位符:{body_code} {body_raw} {body_image}
  {body_jsonlize_spec["a"]["b"]}  —— 按路径取 JSON 字段
变量:baseurl/endpoint/header/body 里用 {{键}} 引用(/pg var 设置;回退同名环境变量)
每个子命令也有独立命令:/pg_run /pg_sched /pg_set ...(输 / 有补全,参数相同)
分组本身也是命令:组名为 a-z0-9_ 时,/组名 [键=值 ...] ≡ /pg run 组名`

type Plugin struct{ plugin.Base }

func (Plugin) Name() string { return "playground" }

// subCommands are also registered as standalone Telegram commands
// (/pg_run, /pg_sched, ...) so clients offer autocompletion with arg hints.
// They route into the same dispatcher as /pg <sub>.
var subCommands = []struct{ name, desc string }{
	{"new", "新建分组:/pg_new <组>"},
	{"list", "列出所有分组"},
	{"show", "查看分组配置:/pg_show <组>"},
	{"del", "删除分组:/pg_del <组>"},
	{"set", "设置字段:/pg_set <组> baseurl|endpoint|method|body|template|threshold <值>"},
	{"header", "管理 header:/pg_header <组> set <键> <值> | del <键>"},
	{"run", "立即执行:/pg_run <组> [键=值 ...]"},
	{"sched", "定时巡检:/pg_sched <组> <cron> [键=值 ...] | off"},
	{"assert", "断言:/pg_assert <组> add <表达式> <==|!=|has> <值> | list | clear"},
	{"var", "变量:/pg_var set <键> <值> | del <键> | list"},
}

func (Plugin) Commands() []plugin.Command {
	admin := []tele.MiddlewareFunc{auth.RequireAdmin()}
	cmds := []plugin.Command{{
		Name:        "pg",
		Description: "HTTP 接口 playground(需 admin):/pg help 看用法",
		Middleware:  admin,
		Handler:     handle,
	}}
	for _, s := range subCommands {
		sub := s.name // capture
		cmds = append(cmds, plugin.Command{
			Name:        "pg_" + sub,
			Description: s.desc,
			Middleware:  admin,
			Handler: func(c tele.Context) error {
				return dispatch(c, sub+" "+c.Message().Payload)
			},
		})
	}
	return cmds
}

// StartSchedule implements plugin.Scheduler: wires up cron health checks and
// result notifications.
func (Plugin) StartSchedule(b *tele.Bot, c *cron.Cron) {
	pg.StartScheduler(c, func(g *pg.Group, ev pg.CheckEvent, resp *pg.Response, failures []string) {
		notify(b, g, ev, resp, failures)
	})
}

// cmdNameRe matches names Telegram accepts as bot commands.
var cmdNameRe = regexp.MustCompile(`^[a-z0-9_]{1,32}$`)

// Wire implements plugin.Wirer: every group whose name is a valid command
// name gets its own /<group> command, equivalent to /pg run <group>.
func (Plugin) Wire(b *tele.Bot) {
	for _, g := range pg.List() {
		registerGroupCommand(b, g.Name)
	}
}

// registerGroupCommand exposes a group as /<name> [key=value ...].
// Invalid names are skipped (the group stays reachable via /pg run).
func registerGroupCommand(b *tele.Bot, name string) {
	if !cmdNameRe.MatchString(name) {
		return
	}
	h := func(c tele.Context) error {
		return handleRun(c, name, strings.Fields(c.Message().Payload))
	}
	desc := "运行分组 " + name + "(可带 键=值 传参)"
	if err := plugin.AddDynamic(b, name, desc, h, auth.RequireAdmin()); err != nil {
		log.Printf("[playground] 组 %s 注册为命令失败: %v", name, err)
	}
}

func handle(c tele.Context) error {
	return dispatch(c, c.Message().Payload)
}

// dispatch routes a "<sub> [args...]" payload, shared by /pg and the
// standalone /pg_<sub> commands.
func dispatch(c tele.Context, payload string) error {
	args := strings.Fields(payload)
	if len(args) == 0 || args[0] == "help" {
		return c.Send(usage)
	}
	switch args[0] {
	case "new":
		if len(args) < 2 {
			return c.Send("用法:/pg new <组>")
		}
		if err := pg.Create(args[1]); err != nil {
			return c.Send("失败:" + err.Error())
		}
		if cmdNameRe.MatchString(args[1]) {
			registerGroupCommand(c.Bot(), args[1])
			return c.Send("✅ 已创建分组 " + args[1] + "(可直接 /" + args[1] + " 执行)")
		}
		return c.Send("✅ 已创建分组 " + args[1])

	case "list":
		gs := pg.List()
		if len(gs) == 0 {
			return c.Send("(暂无分组)")
		}
		var sb strings.Builder
		sb.WriteString("分组:\n")
		for _, g := range gs {
			mark := ""
			if g.Schedule != "" {
				mark = " ⏰" + g.Schedule
			}
			fmt.Fprintf(&sb, "• %s — %s %s%s\n", g.Name, g.Method, urlOf(g), mark)
		}
		return c.Send(sb.String())

	case "show":
		if len(args) < 2 {
			return c.Send("用法:/pg show <组>")
		}
		g, ok := pg.Get(args[1])
		if !ok {
			return c.Send("没有这个组:" + args[1])
		}
		return c.Send(showGroup(g))

	case "del":
		if len(args) < 2 {
			return c.Send("用法:/pg del <组>")
		}
		if err := pg.Delete(args[1]); err != nil {
			return c.Send("失败:" + err.Error())
		}
		plugin.RemoveDynamic(c.Bot(), args[1])
		return c.Send("🗑 已删除 " + args[1])

	case "set":
		return handleSet(c, payload, args)

	case "header":
		return handleHeader(c, payload, args)

	case "sched":
		return handleSched(c, payload, args)

	case "assert":
		return handleAssert(c, payload, args)

	case "var":
		return handleVar(c, payload, args)

	case "run":
		if len(args) < 2 {
			return c.Send("用法:/pg run <组> [键=值 ...]")
		}
		return handleRun(c, args[1], args[2:])

	default:
		return c.Send("未知子命令:" + args[0] + "\n\n" + usage)
	}
}

func handleSet(c tele.Context, payload string, args []string) error {
	if len(args) < 4 {
		return c.Send("用法:/pg set <组> <字段> <值>\n字段:baseurl|endpoint|method|body|template|threshold")
	}
	name, field := args[1], args[2]
	value := rest(payload, 3)
	err := pg.Mutate(name, func(g *pg.Group) error {
		switch strings.ToLower(field) {
		case "baseurl":
			g.BaseURL = value
		case "endpoint":
			g.Endpoint = value
		case "method":
			g.Method = strings.ToUpper(value)
		case "body":
			g.Body = value
		case "template":
			g.Template = value
		case "threshold":
			n, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil || n < 1 {
				return fmt.Errorf("threshold 需为 >=1 的整数")
			}
			g.FailThreshold = n
		default:
			return fmt.Errorf("未知字段 %q(baseurl|endpoint|method|body|template|threshold)", field)
		}
		return nil
	})
	if err != nil {
		return c.Send("失败:" + err.Error())
	}
	return c.Send(fmt.Sprintf("✅ %s.%s 已更新", name, field))
}

func handleHeader(c tele.Context, payload string, args []string) error {
	// header <group> set <key> <value>   |   header <group> del <key>
	if len(args) < 4 {
		return c.Send("用法:\n/pg header <组> set <键> <值>\n/pg header <组> del <键>")
	}
	name, op, key := args[1], args[2], args[3]
	err := pg.Mutate(name, func(g *pg.Group) error {
		switch op {
		case "set":
			g.Headers[key] = rest(payload, 4)
		case "del":
			delete(g.Headers, key)
		default:
			return fmt.Errorf("未知操作 %q(set|del)", op)
		}
		return nil
	})
	if err != nil {
		return c.Send("失败:" + err.Error())
	}
	return c.Send(fmt.Sprintf("✅ %s 的 header 已更新", name))
}

func handleSched(c tele.Context, payload string, args []string) error {
	// sched <group> <cron...> [key=value ...]   |   sched <group> off
	if len(args) < 3 {
		return c.Send("用法:/pg sched <组> <cron> [键=值 ...]  或  /pg sched <组> off")
	}
	name := args[1]
	spec, kv := splitSpecKV(rest(payload, 2))
	if spec == "off" {
		spec, kv = "", nil
	} else if spec == "" && len(kv) > 0 {
		return c.Send("缺少 cron 表达式。用法:/pg sched <组> <cron> [键=值 ...]")
	}
	if err := pg.SetSchedule(name, spec, kv); err != nil {
		return c.Send("失败:" + err.Error())
	}
	if spec == "" {
		return c.Send("⏹ 已关闭 " + name + " 的巡检")
	}
	msg := fmt.Sprintf("⏰ 已设置 %s 巡检:%s", name, spec)
	if len(kv) > 0 {
		msg += "\n传参:" + kvString(kv)
	}
	return c.Send(msg)
}

// splitSpecKV splits "cron spec [key=value ...]" in two: everything from the
// first token containing '=' counts as arguments ('=' never appears in cron
// fields).
func splitSpecKV(s string) (spec string, kv map[string]string) {
	tokens := strings.Fields(s)
	for i, t := range tokens {
		if strings.IndexByte(t, '=') > 0 {
			return strings.Join(tokens[:i], " "), parseKV(tokens[i:])
		}
	}
	return strings.Join(tokens, " "), nil
}

func kvString(m map[string]string) string {
	parts := make([]string, 0, len(m))
	for k, v := range m {
		parts = append(parts, k+"="+v)
	}
	sort.Strings(parts)
	return strings.Join(parts, " ")
}

func handleAssert(c tele.Context, payload string, args []string) error {
	// assert <group> add <expr> <op> <value>  |  assert <group> list  |  assert <group> clear
	if len(args) < 3 {
		return c.Send("用法:\n/pg assert <组> add <表达式> <==|!=|has> <值>\n/pg assert <组> list|clear")
	}
	name, sub := args[1], args[2]
	switch sub {
	case "add":
		if len(args) < 6 {
			return c.Send("用法:/pg assert <组> add <表达式> <==|!=|has> <值>\n例:/pg assert demo add {body_code} == 200")
		}
		expr, op := args[3], args[4]
		want := rest(payload, 5)
		err := pg.Mutate(name, func(g *pg.Group) error {
			g.Asserts = append(g.Asserts, pg.Assert{Expr: expr, Op: op, Want: want})
			return nil
		})
		if err != nil {
			return c.Send("失败:" + err.Error())
		}
		return c.Send(fmt.Sprintf("✅ 已加断言:%s %s %s", expr, op, want))

	case "clear":
		if err := pg.Mutate(name, func(g *pg.Group) error { g.Asserts = nil; return nil }); err != nil {
			return c.Send("失败:" + err.Error())
		}
		return c.Send("✅ 已清空 " + name + " 的断言")

	case "list":
		g, ok := pg.Get(name)
		if !ok {
			return c.Send("没有这个组:" + name)
		}
		if len(g.Asserts) == 0 {
			return c.Send("(无断言)")
		}
		var sb strings.Builder
		sb.WriteString("断言:\n")
		for _, a := range g.Asserts {
			fmt.Fprintf(&sb, "  %s %s %s\n", a.Expr, a.Op, a.Want)
		}
		return c.Send(sb.String())

	default:
		return c.Send("未知操作 " + sub + "(add|list|clear)")
	}
}

func handleVar(c tele.Context, payload string, args []string) error {
	// var set <key> <value>  |  var del <key>  |  var list
	if len(args) < 2 {
		return c.Send("用法:\n/pg var set <键> <值>\n/pg var del <键>\n/pg var list")
	}
	switch args[1] {
	case "set":
		if len(args) < 4 {
			return c.Send("用法:/pg var set <键> <值>")
		}
		if err := pg.SetVar(args[2], rest(payload, 3)); err != nil {
			return c.Send("失败:" + err.Error())
		}
		return c.Send("✅ 已设置变量 " + args[2])
	case "del":
		if len(args) < 3 {
			return c.Send("用法:/pg var del <键>")
		}
		if err := pg.DelVar(args[2]); err != nil {
			return c.Send("失败:" + err.Error())
		}
		return c.Send("🗑 已删除变量 " + args[2])
	case "list":
		keys := pg.Vars()
		if len(keys) == 0 {
			return c.Send("(暂无变量)")
		}
		var sb strings.Builder
		sb.WriteString("变量(值已掩码):\n")
		for _, k := range keys {
			fmt.Fprintf(&sb, "  %s = %s\n", k, maskValue("token", pg.VarValue(k)))
		}
		return c.Send(sb.String())
	default:
		return c.Send("未知操作 " + args[1] + "(set|del|list)")
	}
}

func handleRun(c tele.Context, name string, kv []string) error {
	g, ok := pg.Get(name)
	if !ok {
		return c.Send("没有这个组:" + name)
	}
	if g.BaseURL == "" {
		return c.Send("请先设置 baseurl:/pg set " + name + " baseurl <url>")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	resp, err := pg.Execute(ctx, g, parseKV(kv))
	if err != nil {
		return c.Send("请求失败:" + err.Error())
	}
	text, image := pg.Render(g.Template, resp)
	if image != nil {
		photo := &tele.Photo{File: tele.FromReader(bytes.NewReader(image))}
		photo.Caption = clip(text, 1000)
		return c.Send(photo)
	}
	if text == "" {
		text = "(空响应)"
	}
	return c.Send(clip(text, 4000))
}

// parseKV parses ["k=v", ...] into a temporary variable map for this call.
func parseKV(args []string) map[string]string {
	if len(args) == 0 {
		return nil
	}
	m := make(map[string]string, len(args))
	for _, a := range args {
		if i := strings.IndexByte(a, '='); i > 0 {
			m[a[:i]] = a[i+1:]
		}
	}
	return m
}

// notify pushes a health-check result to all admins (events are already
// debounced).
func notify(b *tele.Bot, g *pg.Group, ev pg.CheckEvent, resp *pg.Response, failures []string) {
	admins := auth.IDs(auth.RoleAdmin)
	if len(admins) == 0 {
		return
	}
	var text string
	var image []byte
	switch ev {
	case pg.EventFail:
		text = "⚠️ 巡检失败 [" + g.Name + "]\n" + strings.Join(failures, "\n")
	case pg.EventRecover:
		text = "✅ 已恢复 [" + g.Name + "]"
	case pg.EventReport:
		if resp != nil {
			text, image = pg.Render(g.Template, resp)
		}
		text = "🔔 [" + g.Name + "]\n" + text
	default:
		return
	}
	for _, id := range admins {
		if image != nil {
			photo := &tele.Photo{File: tele.FromReader(bytes.NewReader(image))}
			photo.Caption = clip(text, 1000)
			_, _ = b.Send(tele.ChatID(id), photo)
		} else {
			_, _ = b.Send(tele.ChatID(id), clip(text, 4000))
		}
	}
}

func urlOf(g *pg.Group) string {
	if g.BaseURL == "" {
		return "(未设 baseurl)"
	}
	return g.BaseURL + g.Endpoint
}

func showGroup(g *pg.Group) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "组:%s\nBaseURL:%s\n接口:%s\n方式:%s\n", g.Name, g.BaseURL, g.Endpoint, g.Method)
	if g.Body != "" {
		fmt.Fprintf(&sb, "请求体:%s\n", g.Body)
	}
	sb.WriteString("Headers:\n")
	if len(g.Headers) == 0 {
		sb.WriteString("  (无)\n")
	}
	for k, v := range g.Headers {
		fmt.Fprintf(&sb, "  %s: %s\n", k, maskValue(k, v))
	}
	if g.Schedule != "" {
		fmt.Fprintf(&sb, "巡检:%s", g.Schedule)
		if len(g.ScheduleArgs) > 0 {
			fmt.Fprintf(&sb, "(传参 %s)", kvString(g.ScheduleArgs))
		}
		if g.FailThreshold > 1 {
			fmt.Fprintf(&sb, "(连续失败 %d 次告警)", g.FailThreshold)
		}
		sb.WriteString("\n")
	}
	if len(g.Asserts) > 0 {
		sb.WriteString("断言:\n")
		for _, a := range g.Asserts {
			fmt.Fprintf(&sb, "  %s %s %s\n", a.Expr, a.Op, a.Want)
		}
	}
	fmt.Fprintf(&sb, "模板:\n%s", g.Template)
	return sb.String()
}

// maskValue masks sensitive header values (authorization/token/key/secret)
// for display.
func maskValue(key, val string) string {
	k := strings.ToLower(key)
	if strings.Contains(k, "auth") || strings.Contains(k, "token") ||
		strings.Contains(k, "key") || strings.Contains(k, "secret") {
		r := []rune(val)
		if len(r) <= 4 {
			return "***"
		}
		return string(r[:2]) + "***" + string(r[len(r)-2:])
	}
	return val
}

// rest skips the first skip whitespace-separated tokens of the payload and
// returns the remaining raw string (spaces and newlines preserved).
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
