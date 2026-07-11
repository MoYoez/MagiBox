package cf

import (
	"context"
	"fmt"
	"strings"
	"time"

	tele "gopkg.in/telebot.v3"

	cf "github.com/moyoez/magibox/internal/cloudflare"
	kuma "github.com/moyoez/magibox/internal/kuma"
)

// provisionTimeout bounds the whole one-shot provision (CF bind + several Kuma
// calls), which involves multiple round-trips.
const provisionTimeout = 60 * time.Second

// notificationName builds the deterministic webhook-notification name a rule
// owns in Kuma, so provision reuses one notification per rule instead of
// creating a duplicate on every run.
func notificationName(ruleName string) string { return "magibox-" + ruleName }

// handleProvision is the /cf provision <worker> [域名] telebot boundary: it
// parses args, runs the provision orchestration, and sends the result. All the
// logic lives in provision so it is testable without a telebot Context.
func handleProvision(c tele.Context, args []string) error {
	if len(args) < 2 {
		return c.Send("用法:/cf provision <worker> [域名]\n" +
			"不给域名 → 从该 worker 大类里取一个「未使用」域名\n" +
			"一步完成:校验 CF → 绑定 Custom Domain → 建/复用 Kuma 监控 → 挂上掉线切换 webhook\n" +
			"前置:该 worker 需已有 failover 规则,且已 /cf failover kuma <规则> <kuma凭据>")
	}
	domain := ""
	if len(args) >= 3 {
		domain = args[2]
	}
	ctx, cancel := context.WithTimeout(context.Background(), provisionTimeout)
	defer cancel()
	return c.Send(provision(ctx, args[1], domain))
}

// provision pushes one domain live end-to-end and returns the user-facing
// report. It binds the domain as a Custom Domain, then makes sure Uptime Kuma
// has a monitor for it wired to the worker's failover webhook — so a newly
// provisioned domain is auto-failover-covered without any manual Kuma clicks.
//
// It leans on the worker's failover rule for the Kuma cred and callback token,
// so a rule (with a Kuma cred set via /cf failover kuma) must exist first. The
// bind happens before the Kuma calls so a later Kuma hiccup leaves a working
// (if unmonitored) domain rather than an orphan monitor; re-running provision
// then reconciles the monitor idempotently.
func provision(ctx context.Context, workerName, domain string) string {
	rule, ok := cf.FailoverByWorker(workerName)
	if !ok {
		return fmt.Sprintf("worker %s 还没有 failover 规则,无法自动挂监控。\n先建规则:/cf failover add <名> %s\n再设 Kuma 凭据:/cf failover kuma <名> <kuma凭据>", workerName, workerName)
	}
	if rule.KumaCred == "" {
		return fmt.Sprintf("规则 %s 还没绑定 Kuma 凭据,无法建监控。\n先执行:/cf failover kuma %s <kuma凭据>", rule.Name, rule.Name)
	}
	kcred, ok := kuma.GetCred(rule.KumaCred)
	if !ok {
		return fmt.Sprintf("规则 %s 绑定的 Kuma 凭据 %s 不存在,先 /kuma cred add", rule.Name, rule.KumaCred)
	}

	// Pre-flight: for an explicitly given domain, verify it is bindable on CF
	// before touching anything, so the user gets a targeted error. Auto-picked
	// domains come from the curated pool and BindDomain validates them anyway.
	if domain != "" {
		if !cf.HostValid(domain) {
			return "域名格式不对:" + domain
		}
		if _, _, err := cf.ResolveWorkerZone(ctx, workerName, domain); err != nil {
			return "这个域名在 CF 上不可绑定:" + err.Error()
		}
	}

	// Step 1 — bind the Custom Domain (also marks the record used).
	bindRes, err := cf.BindDomain(ctx, workerName, domain, false)
	if err != nil {
		return "绑定失败:" + err.Error()
	}
	live := bindRes.Domain

	var sb strings.Builder
	fmt.Fprintf(&sb, "✅ 已上线 %s → worker「%s」(zone %s)\n", live, bindRes.Worker, bindRes.ZoneName)
	if bindRes.AutoPicked {
		sb.WriteString("(域名从大类「未使用」池中自动选取)\n")
	}

	// Step 2 — ensure a Kuma monitor named after the domain (reuse a hand-made
	// one). The failover receiver matches on monitor.name == domain, so the
	// name must be exactly the hostname.
	client := kuma.NewClient(kcred)
	mon, found, err := client.FindMonitorByName(ctx, live)
	if err != nil {
		sb.WriteString("⚠️ 无法查询 Kuma 监控(域名已上线,但未挂监控):" + err.Error())
		return sb.String()
	}
	monitorID := mon.ID
	if found {
		fmt.Fprintf(&sb, "• 复用已有监控 #%d\n", monitorID)
	} else {
		id, _, err := client.CreateMonitor(ctx, live, "https://"+live)
		if err != nil {
			sb.WriteString("⚠️ 建监控失败(域名已上线,但未挂监控):" + err.Error())
			return sb.String()
		}
		monitorID = id
		fmt.Fprintf(&sb, "• 已建监控 #%d(https://%s)\n", monitorID, live)
	}
	if monitorID == 0 {
		sb.WriteString("⚠️ Kuma 未返回监控 id,无法自动挂 webhook,请到 Kuma 手动为该监控挂通知。")
		return sb.String()
	}

	// Step 3 — ensure the rule's webhook notification exists (reuse by name).
	notifName := notificationName(rule.Name)
	notif, found, err := client.FindNotificationByName(ctx, notifName)
	if err != nil {
		sb.WriteString("⚠️ 无法查询 Kuma 通知:" + err.Error())
		return sb.String()
	}
	notifID := notif.ID
	if found {
		fmt.Fprintf(&sb, "• 复用通知「%s」#%d\n", notifName, notifID)
	} else {
		id, err := client.CreateWebhookNotification(ctx, notifName, callbackURL(rule.Token))
		if err != nil {
			sb.WriteString("⚠️ 建 webhook 通知失败:" + err.Error())
			return sb.String()
		}
		notifID = id
		fmt.Fprintf(&sb, "• 已建 webhook 通知「%s」#%d\n", notifName, notifID)
	}
	if notifID == 0 {
		sb.WriteString("⚠️ Kuma 未返回通知 id,无法自动绑定,请到 Kuma 手动把通知挂到该监控。")
		return sb.String()
	}

	// Step 4 — wire the notification onto the monitor so downs reach the bot.
	// The wrapper attaches by monitor name (== the domain), not by id.
	if err := client.SetMonitorNotificationsByName(ctx, live, []int{notifID}); err != nil {
		sb.WriteString("⚠️ 绑定通知到监控失败:" + err.Error())
		return sb.String()
	}
	fmt.Fprintf(&sb, "• 已把通知挂到监控 #%d\n", monitorID)
	fmt.Fprintf(&sb, "\n🎉 %s 已全自动纳管:掉线将按规则「%s」(%s,阈值 %d)处理", live, rule.Name, rule.Mode, rule.Threshold)
	return sb.String()
}
