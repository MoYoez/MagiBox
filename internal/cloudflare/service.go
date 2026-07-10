package cloudflare

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// SetDomainField sets one field on a domain record by name (English or Chinese
// alias). Shared by the /cf domain set command and the panel so both validate
// and behave identically. Supported fields: category, sub, status, purchased,
// usage, dns, ready, changed.
func SetDomainField(domain, field, value string) error {
	return MutateDomain(domain, func(d *Domain) error {
		switch strings.ToLower(strings.TrimSpace(field)) {
		case "category", "大类":
			if !NameValid(value) {
				return fmt.Errorf("大类名只能是 a-zA-Z0-9_.-")
			}
			d.Category = value
		case "sub", "小类":
			d.Sub = value
		case "status", "状态":
			s, ok := NormalizeStatus(value)
			if !ok {
				return fmt.Errorf("状态只能是 未使用|已使用|ban")
			}
			d.Status = s
			if s == StatusUnused {
				d.Worker = ""
			}
		case "purchased", "购买", "购买日期":
			d.PurchasedAt = value
		case "usage", "用途", "用在哪":
			d.Usage = value
		case "dns", "解析":
			d.DNS = value
		case "changed", "更换", "更换时间":
			d.ChangedAt = value
		case "ready", "就绪":
			b, ok := parseReady(value)
			if !ok {
				return fmt.Errorf("ready 需为 yes/no(或 true/false、1/0)")
			}
			d.Ready = b
		default:
			return fmt.Errorf("未知字段 %q(category|sub|status|purchased|usage|dns|ready|changed)", field)
		}
		return nil
	})
}

// parseReady interprets a readiness value (English or Chinese).
func parseReady(s string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "yes", "y", "true", "1", "就绪", "是", "ok":
		return true, true
	case "no", "n", "false", "0", "未就绪", "否":
		return false, true
	}
	return false, false
}

// ConflictError reports that a hostname is already attached to a different
// worker and force was not set.
type ConflictError struct {
	Domain        string
	CurrentWorker string
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("%s 已绑定到 worker %s", e.Domain, e.CurrentWorker)
}

// BindResult describes a successful bind.
type BindResult struct {
	Domain     string
	Worker     string
	ZoneName   string
	AutoPicked bool // domain was auto-selected from the worker's category
}

// BindDomain attaches a domain to a worker as a Custom Domain and updates the
// record book. If domain is empty it auto-picks the next unused domain from the
// worker's category. It resolves the zone from the hostname, refuses to
// re-point a hostname bound to another worker unless force is set (returning a
// *ConflictError), attaches, then marks the record used and stamps ChangedAt.
//
// Shared by the /cf bind command and the panel so both behave identically.
func BindDomain(ctx context.Context, workerName, domain string, force bool) (*BindResult, error) {
	w, ok := GetWorker(workerName)
	if !ok {
		return nil, fmt.Errorf("没有这个 worker:%s", workerName)
	}
	autoPicked := false
	if domain == "" {
		if w.Category == "" {
			return nil, fmt.Errorf("worker %s 未绑定大类,请显式给域名", workerName)
		}
		d, ok := NextUnused(w.Category)
		if !ok {
			return nil, fmt.Errorf("大类 %s 里没有「未使用」的域名了", w.Category)
		}
		domain, autoPicked = d.Name, true
	}
	if !hostRe.MatchString(domain) {
		return nil, fmt.Errorf("域名格式不对:%s", domain)
	}
	cred, ok := GetCred(w.Cred)
	if !ok {
		return nil, fmt.Errorf("worker 绑定的凭据 %s 不存在", w.Cred)
	}
	client := NewClient(cred)

	existing, err := client.ListWorkerDomainsByHostname(ctx, domain)
	if err != nil {
		return nil, fmt.Errorf("查询现有绑定失败: %w", err)
	}
	for _, e := range existing {
		if e.Service != w.Name && !force {
			return nil, &ConflictError{Domain: domain, CurrentWorker: e.Service}
		}
	}
	zoneID, zoneName, err := client.ResolveZoneID(ctx, domain)
	if err != nil {
		return nil, fmt.Errorf("解析 zone 失败: %w", err)
	}
	if _, err := client.AttachWorkerDomain(ctx, domain, w.Name, zoneID, w.Env); err != nil {
		return nil, fmt.Errorf("绑定失败: %w", err)
	}

	if _, ok := GetDomain(domain); !ok {
		cat := w.Category
		if cat == "" {
			cat = "default"
		}
		_ = AddDomain(cat, domain, "")
	}
	_ = MutateDomain(domain, func(d *Domain) error {
		d.Status = StatusUsed
		d.Worker = w.Name
		d.ChangedAt = time.Now().Format("2006-01-02 15:04")
		return nil
	})
	return &BindResult{Domain: domain, Worker: w.Name, ZoneName: zoneName, AutoPicked: autoPicked}, nil
}

// UnbindDomain detaches a domain's Custom Domain binding (via the worker it is
// recorded against) and returns the record to unused.
func UnbindDomain(ctx context.Context, domain string) error {
	d, ok := GetDomain(domain)
	if !ok {
		return fmt.Errorf("没有这个域名记录:%s", domain)
	}
	if d.Worker == "" {
		return fmt.Errorf("%s 记录里没有绑定的 worker,无从解绑", domain)
	}
	w, ok := GetWorker(d.Worker)
	if !ok {
		return fmt.Errorf("记录指向的 worker %s 已不存在", d.Worker)
	}
	cred, ok := GetCred(w.Cred)
	if !ok {
		return fmt.Errorf("worker 的凭据 %s 已不存在", w.Cred)
	}
	client := NewClient(cred)
	existing, err := client.ListWorkerDomainsByHostname(ctx, domain)
	if err != nil {
		return fmt.Errorf("查询绑定失败: %w", err)
	}
	if len(existing) == 0 {
		return fmt.Errorf("Cloudflare 上没有找到 %s 的 Custom Domain 绑定(可能已解绑)", domain)
	}
	for _, e := range existing {
		if err := client.DeleteWorkerDomain(ctx, e.ID); err != nil {
			return fmt.Errorf("解绑失败: %w", err)
		}
	}
	_ = MutateDomain(domain, func(d *Domain) error {
		d.Status = StatusUnused
		d.Worker = ""
		return nil
	})
	return nil
}
