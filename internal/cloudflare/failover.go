package cloudflare

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"sync"
)

// FailoverMode is how a rule reacts once a domain crosses its down threshold.
type FailoverMode string

const (
	// FailoverAuto switches to a healthy domain the moment the threshold is hit.
	FailoverAuto FailoverMode = "auto"
	// FailoverManual only alerts on threshold; the switch waits for an explicit
	// /cf failover apply.
	FailoverManual FailoverMode = "manual"
)

// DefaultThreshold is the consecutive-down count a rule uses when unset.
const DefaultThreshold = 2

// FailoverHookPrefix is the shared HTTP path prefix for a rule's Uptime Kuma
// callback. The cf plugin mounts the receiver here and both the bot and the
// panel build a rule's callback URL as PublicBaseURL + FailoverHookPrefix +
// token, so the two stay in lockstep.
const FailoverHookPrefix = "/hook/cf-failover/"

// FailoverRule ties an Uptime Kuma callback to one worker: when a domain bound
// to that worker goes down enough times, the rule detaches it and attaches a
// healthy domain from the same worker's category pool.
type FailoverRule struct {
	Name      string       `json:"name"`                // identifier, matches nameRe
	Token     string       `json:"token"`               // secret path segment of the callback URL
	Worker    string       `json:"worker"`              // worker whose domains this rule guards
	Target    int64        `json:"target"`              // chat id to notify (0 = not set yet)
	Threshold int          `json:"threshold"`           // consecutive downs before acting
	Mode      FailoverMode `json:"mode"`                // auto | manual
	KumaCred  string       `json:"kuma_cred,omitempty"` // Kuma wrapper cred used to create monitors during provision
}

// NormalizeMode maps a user-supplied mode to a canonical value.
func NormalizeMode(s string) (FailoverMode, bool) {
	switch FailoverMode(s) {
	case FailoverAuto:
		return FailoverAuto, true
	case FailoverManual:
		return FailoverManual, true
	}
	return "", false
}

// --- Failover rule CRUD (persisted in the shared store) ---

// AddFailover registers a rule guarding worker, with a freshly generated
// callback token. threshold <= 0 falls back to DefaultThreshold; an empty mode
// falls back to manual (safe default).
func AddFailover(name, worker string, threshold int, mode FailoverMode) (*FailoverRule, error) {
	if !nameRe.MatchString(name) {
		return nil, fmt.Errorf("规则名只能是 a-zA-Z0-9_.- 且不超过 64 位")
	}
	if threshold <= 0 {
		threshold = DefaultThreshold
	}
	if mode == "" {
		mode = FailoverManual
	}
	def.mu.Lock()
	defer def.mu.Unlock()
	if _, ok := def.workers[worker]; !ok {
		return nil, fmt.Errorf("没有这个 worker:%s", worker)
	}
	if _, ok := def.failovers[name]; ok {
		return nil, fmt.Errorf("已存在同名规则:%s", name)
	}
	r := &FailoverRule{Name: name, Token: newFailoverToken(), Worker: worker, Threshold: threshold, Mode: mode}
	def.failovers[name] = r
	if err := def.save(); err != nil {
		return nil, err
	}
	return cloneFailover(r), nil
}

// GetFailover returns a copy of the named rule.
func GetFailover(name string) (*FailoverRule, bool) {
	def.mu.RLock()
	defer def.mu.RUnlock()
	r, ok := def.failovers[name]
	if !ok {
		return nil, false
	}
	return cloneFailover(r), true
}

// GetFailoverByToken returns a copy of the rule owning token (callback lookup).
func GetFailoverByToken(token string) (*FailoverRule, bool) {
	def.mu.RLock()
	defer def.mu.RUnlock()
	for _, r := range def.failovers {
		if r.Token == token {
			return cloneFailover(r), true
		}
	}
	return nil, false
}

// FailoverByWorker returns a copy of the rule guarding worker, if any. When a
// worker has more than one rule (unusual) the lexicographically first by name
// is returned for determinism.
func FailoverByWorker(worker string) (*FailoverRule, bool) {
	def.mu.RLock()
	defer def.mu.RUnlock()
	var found *FailoverRule
	for _, r := range def.failovers {
		if r.Worker != worker {
			continue
		}
		if found == nil || r.Name < found.Name {
			found = r
		}
	}
	if found == nil {
		return nil, false
	}
	return cloneFailover(found), true
}

// ListFailovers returns copies of all rules, sorted by name.
func ListFailovers() []*FailoverRule {
	def.mu.RLock()
	defer def.mu.RUnlock()
	out := make([]*FailoverRule, 0, len(def.failovers))
	for _, r := range def.failovers {
		out = append(out, cloneFailover(r))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// DelFailover removes a rule and any in-memory pending state it holds.
func DelFailover(name string) error {
	def.mu.Lock()
	defer def.mu.Unlock()
	if _, ok := def.failovers[name]; !ok {
		return fmt.Errorf("没有这个规则:%s", name)
	}
	delete(def.failovers, name)
	if err := def.save(); err != nil {
		return err
	}
	fstate.clearPending(name)
	return nil
}

// MutateFailover applies fn to the named rule under lock and persists. The
// token is never mutated here, so the callback lookup stays valid.
func MutateFailover(name string, fn func(*FailoverRule) error) error {
	def.mu.Lock()
	defer def.mu.Unlock()
	r, ok := def.failovers[name]
	if !ok {
		return fmt.Errorf("没有这个规则:%s", name)
	}
	if err := fn(r); err != nil {
		return err
	}
	return def.save()
}

func cloneFailover(r *FailoverRule) *FailoverRule {
	c := *r
	return &c
}

func newFailoverToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// --- In-memory volatile state (consecutive-down counts + pending switches) ---
//
// This is deliberately not persisted: a down streak is transient and a restart
// resetting it to zero is the safe behavior.

type failoverState struct {
	mu         sync.Mutex
	downCounts map[string]int    // domain -> consecutive down count
	pending    map[string]string // rule name -> bad domain awaiting manual apply
}

var fstate = &failoverState{
	downCounts: map[string]int{},
	pending:    map[string]string{},
}

// resetFailoverState clears the volatile counters (used by tests).
func resetFailoverState() {
	fstate.mu.Lock()
	defer fstate.mu.Unlock()
	fstate.downCounts = map[string]int{}
	fstate.pending = map[string]string{}
}

// RecordDown increments the consecutive-down count for domain and reports
// whether it now meets the rule's threshold (the trigger point).
func RecordDown(rule *FailoverRule, domain string) (count int, fire bool) {
	fstate.mu.Lock()
	defer fstate.mu.Unlock()
	fstate.downCounts[domain]++
	count = fstate.downCounts[domain]
	return count, count >= rule.Threshold
}

// RecordUp clears the consecutive-down count for domain (it recovered).
func RecordUp(domain string) {
	fstate.mu.Lock()
	defer fstate.mu.Unlock()
	delete(fstate.downCounts, domain)
}

// SetPending records that rule is awaiting a manual apply to replace badDomain.
func SetPending(rule, badDomain string) {
	fstate.mu.Lock()
	defer fstate.mu.Unlock()
	fstate.pending[rule] = badDomain
}

// Pending returns the bad domain awaiting manual apply for rule, if any.
func Pending(rule string) (string, bool) {
	fstate.mu.Lock()
	defer fstate.mu.Unlock()
	d, ok := fstate.pending[rule]
	return d, ok
}

// clearPending drops any pending switch recorded for rule.
func (s *failoverState) clearPending(rule string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.pending, rule)
}

// ClearPending drops any pending switch recorded for rule (after apply/recovery).
func ClearPending(rule string) { fstate.clearPending(rule) }

// --- Switch orchestration ---

// SwitchResult describes a completed domain switch.
type SwitchResult struct {
	Worker string
	Bad    string
	New    string
}

// SwitchWorkerDomain detaches badDomain from its worker (marking it banned) and
// attaches the next unused domain from that worker's category pool. It reuses
// the existing bind/detach plumbing so behavior matches /cf bind and unbind.
//
// Called by auto rules the moment the threshold is hit, and by /cf failover
// apply for manual rules — the same logic for both.
func SwitchWorkerDomain(ctx context.Context, workerName, badDomain string) (*SwitchResult, error) {
	w, ok := GetWorker(workerName)
	if !ok {
		return nil, fmt.Errorf("没有这个 worker:%s", workerName)
	}
	if w.Category == "" {
		return nil, fmt.Errorf("worker %s 未绑定大类,无从挑选备用域名", workerName)
	}
	cred, ok := GetCred(w.Cred)
	if !ok {
		return nil, fmt.Errorf("worker 绑定的凭据 %s 不存在", w.Cred)
	}
	// Pick the replacement before touching Cloudflare so an empty pool aborts
	// cleanly without leaving the worker detached.
	next, ok := NextUnused(w.Category)
	if !ok {
		return nil, fmt.Errorf("大类 %s 里没有「未使用」的域名了,需人工介入", w.Category)
	}

	client := NewClient(cred)
	if err := detachHostname(ctx, client, badDomain); err != nil {
		return nil, err
	}
	// Ban the bad domain so it never gets auto-picked again.
	_ = MutateDomain(badDomain, func(d *Domain) error {
		d.Status = StatusBanned
		d.Worker = ""
		return nil
	})

	if _, err := BindDomain(ctx, workerName, next.Name, false); err != nil {
		return nil, fmt.Errorf("绑定备用域名 %s 失败: %w", next.Name, err)
	}
	RecordUp(badDomain) // clear the streak; the domain is out of rotation now
	return &SwitchResult{Worker: workerName, Bad: badDomain, New: next.Name}, nil
}

// detachHostname removes every Custom Domain attachment for hostname on
// Cloudflare. Shared shape with UnbindDomain's detach half, but this one leaves
// the record-book status to the caller (SwitchWorkerDomain bans it; unbind
// frees it).
func detachHostname(ctx context.Context, client *Client, hostname string) error {
	existing, err := client.ListWorkerDomainsByHostname(ctx, hostname)
	if err != nil {
		return fmt.Errorf("查询绑定失败: %w", err)
	}
	for _, e := range existing {
		if err := client.DeleteWorkerDomain(ctx, e.ID); err != nil {
			return fmt.Errorf("解绑失败: %w", err)
		}
	}
	return nil
}
