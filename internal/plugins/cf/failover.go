package cf

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	tele "gopkg.in/telebot.v3"

	cf "github.com/moyoez/magibox/internal/cloudflare"
	"github.com/moyoez/magibox/internal/config"
	"github.com/moyoez/magibox/pkg/plugin"
)

const (
	hookPrefix   = cf.FailoverHookPrefix
	maxBodyBytes = 1 << 20 // read limit per inbound webhook
	switchDelay  = 25 * time.Second
)

// HTTPRoutes implements plugin.HTTPer: mount the Uptime Kuma callback receiver
// on the shared HTTP server. Same shape as the uptime plugin's webhook route.
func (Plugin) HTTPRoutes(b *tele.Bot) []plugin.Route {
	return []plugin.Route{{
		Pattern: hookPrefix,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			handleHook(b, w, r)
		}),
	}}
}

// handleHook receives an Uptime Kuma webhook for a failover rule. The Kuma
// monitor name is the domain (monitors are created with the domain as their
// name), and heartbeat.status is 0=down / 1=up. It answers 200 quickly so Kuma
// does not retry, then acts on the down/up transition.
func handleHook(b *tele.Bot, w http.ResponseWriter, r *http.Request) {
	token := strings.Trim(strings.TrimPrefix(r.URL.Path, hookPrefix), "/")
	rule, ok := cf.GetFailoverByToken(token)
	if token == "" || !ok {
		http.NotFound(w, r)
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, "ok")

	domain, status := parseKuma(body)
	if domain == "" {
		log.Printf("[cf-failover] 规则 %s 收到回调但无法解析 monitor 域名,已忽略", rule.Name)
		return
	}

	switch status {
	case "1": // up: the domain recovered
		cf.RecordUp(domain)
		return
	case "0": // down
		count, fire := cf.RecordDown(rule, domain)
		if !fire {
			return // below threshold; stay quiet until it trips
		}
		onThreshold(b, rule, domain, count)
	default:
		// Non-heartbeat payloads (e.g. test pings) carry no status; ignore.
	}
}

// onThreshold runs when a domain has crossed the rule's consecutive-down
// threshold: auto rules switch immediately, manual rules park a pending switch
// and alert.
func onThreshold(b *tele.Bot, rule *cf.FailoverRule, domain string, count int) {
	if rule.Mode == cf.FailoverAuto {
		ctx, cancel := context.WithTimeout(context.Background(), switchDelay)
		defer cancel()
		res, err := cf.SwitchWorkerDomain(ctx, rule.Worker, domain)
		if err != nil {
			notify(b, rule.Target, fmt.Sprintf("⚠️ [%s] %s 连续 %d 次掉线,自动切换失败:%s", rule.Name, domain, count, err.Error()))
			return
		}
		notify(b, rule.Target, fmt.Sprintf("🔁 [%s] %s 连续 %d 次掉线,已自动切换:%s → %s(worker %s)", rule.Name, domain, count, res.Bad, res.New, res.Worker))
		return
	}
	// manual: park it and ask for confirmation.
	cf.SetPending(rule.Name, domain)
	notify(b, rule.Target, fmt.Sprintf("⚠️ [%s] %s 连续 %d 次掉线。确认更换请发:\n/cf failover apply %s", rule.Name, domain, count, rule.Name))
}

// parseKuma pulls the monitor name (used as the domain) and heartbeat status
// out of an Uptime Kuma webhook body. Missing fields come back empty.
func parseKuma(body []byte) (domain, status string) {
	var data any
	if sonic.Unmarshal(body, &data) != nil {
		return "", ""
	}
	domain = kumaLookup(data, "monitor", "name")
	status = kumaLookup(data, "heartbeat", "status")
	return domain, status
}

// kumaLookup walks a two-level object path (obj.key) and stringifies the leaf.
func kumaLookup(data any, obj, key string) string {
	m, ok := data.(map[string]any)
	if !ok {
		return ""
	}
	sub, ok := m[obj].(map[string]any)
	if !ok {
		return ""
	}
	switch v := sub[key].(type) {
	case string:
		return v
	case float64:
		if v == float64(int64(v)) {
			return strconv.FormatInt(int64(v), 10)
		}
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(v)
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", v)
	}
}

func notify(b *tele.Bot, target int64, text string) {
	if b == nil || target == 0 {
		log.Printf("[cf-failover] %s", text)
		return
	}
	if _, err := b.Send(tele.ChatID(target), text); err != nil {
		log.Printf("[cf-failover] 推送到 %d 失败: %v", target, err)
	}
}

// --- /cf failover command family (admin, covered by /cf's RequireAdmin) ---

func handleFailover(c tele.Context, args []string) error {
	if len(args) < 2 {
		return c.Send("用法:/cf failover add <名> <worker> [阈值] [auto|manual] | list | show <名> | target <名> <chat_id|here> | mode <名> <auto|manual> | apply <名> | test <名> <域名> | del <名>")
	}
	switch args[1] {
	case "add":
		return failoverAdd(c, args)
	case "list":
		return failoverList(c)
	case "show":
		return failoverShow(c, args)
	case "target":
		return failoverTarget(c, args)
	case "mode":
		return failoverMode(c, args)
	case "apply":
		return failoverApply(c, args)
	case "test":
		return failoverTest(c, args)
	case "del":
		if len(args) < 3 {
			return c.Send("用法:/cf failover del <名>")
		}
		if err := cf.DelFailover(args[2]); err != nil {
			return c.Send("失败:" + err.Error())
		}
		return c.Send("🗑 已删除规则 " + args[2])
	default:
		return c.Send("未知操作 " + args[1] + "(add|list|show|target|mode|apply|test|del)")
	}
}

func failoverAdd(c tele.Context, args []string) error {
	if len(args) < 4 {
		return c.Send("用法:/cf failover add <名> <worker> [阈值] [auto|manual]")
	}
	threshold := cf.DefaultThreshold
	mode := cf.FailoverManual
	for _, a := range args[4:] {
		if n, err := strconv.Atoi(a); err == nil {
			threshold = n
			continue
		}
		if m, ok := cf.NormalizeMode(a); ok {
			mode = m
			continue
		}
		return c.Send("无法识别的参数:" + a + "(阈值需为整数,模式为 auto|manual)")
	}
	r, err := cf.AddFailover(args[2], args[3], threshold, mode)
	if err != nil {
		return c.Send("失败:" + err.Error())
	}
	return c.Send(fmt.Sprintf("✅ 已创建规则 %s(worker %s,阈值 %d,%s)\n回调地址(填到 Uptime Kuma 对应 monitor 的 Webhook 通知):\n%s\n\n下一步:/cf failover target %s here 绑定通知目标",
		r.Name, r.Worker, r.Threshold, r.Mode, callbackURL(r.Token), r.Name))
}

func failoverList(c tele.Context) error {
	rules := cf.ListFailovers()
	if len(rules) == 0 {
		return c.Send("(暂无规则)。/cf failover add <名> <worker> [阈值] [auto|manual]")
	}
	var sb strings.Builder
	sb.WriteString("掉线切换规则:\n")
	for _, r := range rules {
		line := fmt.Sprintf("• %s — worker %s,阈值 %d,%s,目标 %s", r.Name, r.Worker, r.Threshold, r.Mode, targetText(r.Target))
		if pend, ok := cf.Pending(r.Name); ok {
			line += "  ⏳待确认:" + pend
		}
		sb.WriteString(line + "\n")
	}
	return c.Send(sb.String())
}

func failoverShow(c tele.Context, args []string) error {
	if len(args) < 3 {
		return c.Send("用法:/cf failover show <名>")
	}
	r, ok := cf.GetFailover(args[2])
	if !ok {
		return c.Send("没有这个规则:" + args[2])
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "规则:%s\nworker:%s\n阈值:连续 %d 次掉线\n模式:%s\n通知目标:%s\n回调地址:%s\n",
		r.Name, r.Worker, r.Threshold, r.Mode, targetText(r.Target), callbackURL(r.Token))
	if pend, ok := cf.Pending(r.Name); ok {
		fmt.Fprintf(&sb, "待确认更换:%s(发 /cf failover apply %s 执行)\n", pend, r.Name)
	}
	return c.Send(strings.TrimSpace(sb.String()))
}

func failoverTarget(c tele.Context, args []string) error {
	if len(args) < 4 {
		return c.Send("用法:/cf failover target <名> <chat_id|here>")
	}
	var target int64
	if args[3] == "here" {
		target = c.Chat().ID
	} else {
		id, err := strconv.ParseInt(args[3], 10, 64)
		if err != nil {
			return c.Send("chat_id 需为整数,或用 here 表示当前会话")
		}
		target = id
	}
	if err := cf.MutateFailover(args[2], func(r *cf.FailoverRule) error {
		r.Target = target
		return nil
	}); err != nil {
		return c.Send("失败:" + err.Error())
	}
	return c.Send(fmt.Sprintf("✅ %s 的通知目标已设为 %d", args[2], target))
}

func failoverMode(c tele.Context, args []string) error {
	if len(args) < 4 {
		return c.Send("用法:/cf failover mode <名> <auto|manual>")
	}
	mode, ok := cf.NormalizeMode(args[3])
	if !ok {
		return c.Send("模式只能是 auto|manual")
	}
	if err := cf.MutateFailover(args[2], func(r *cf.FailoverRule) error {
		r.Mode = mode
		return nil
	}); err != nil {
		return c.Send("失败:" + err.Error())
	}
	return c.Send("✅ " + args[2] + " 的模式已设为 " + string(mode))
}

func failoverApply(c tele.Context, args []string) error {
	if len(args) < 3 {
		return c.Send("用法:/cf failover apply <名>")
	}
	r, ok := cf.GetFailover(args[2])
	if !ok {
		return c.Send("没有这个规则:" + args[2])
	}
	pend, ok := cf.Pending(r.Name)
	if !ok {
		return c.Send("规则 " + r.Name + " 当前没有待确认的切换")
	}
	ctx, cancel := context.WithTimeout(context.Background(), switchDelay)
	defer cancel()
	res, err := cf.SwitchWorkerDomain(ctx, r.Worker, pend)
	if err != nil {
		return c.Send("切换失败:" + err.Error())
	}
	cf.ClearPending(r.Name)
	return c.Send(fmt.Sprintf("✅ 已切换:%s → %s(worker %s),%s 已标记为 ban", res.Bad, res.New, res.Worker, res.Bad))
}

func failoverTest(c tele.Context, args []string) error {
	if len(args) < 4 {
		return c.Send("用法:/cf failover test <名> <域名>")
	}
	r, ok := cf.GetFailover(args[2])
	if !ok {
		return c.Send("没有这个规则:" + args[2])
	}
	ctx, cancel := context.WithTimeout(context.Background(), switchDelay)
	defer cancel()
	res, err := cf.SwitchWorkerDomain(ctx, r.Worker, args[3])
	if err != nil {
		return c.Send("演练失败:" + err.Error())
	}
	return c.Send(fmt.Sprintf("✅ 演练切换:%s → %s(worker %s)", res.Bad, res.New, res.Worker))
}

func callbackURL(token string) string {
	return strings.TrimRight(config.PublicBaseURL(), "/") + hookPrefix + token
}

func targetText(target int64) string {
	if target == 0 {
		return "(未设)"
	}
	return strconv.FormatInt(target, 10)
}
