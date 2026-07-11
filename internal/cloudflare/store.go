// Package cloudflare is the core of the /cf plugin: it stores Cloudflare API
// credentials (multi-cred), registered Workers, and a domain record book
// (major category / minor category / status), and talks to the Cloudflare API
// to attach and detach Worker Custom Domains.
//
// It carries no Telegram dependency; the plugin glue in internal/plugins/cf
// drives it from commands.
package cloudflare

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/bytedance/sonic"
)

// Domain status values. Chinese aliases are accepted at the command layer.
const (
	StatusUnused = "unused"
	StatusUsed   = "used"
	StatusBanned = "banned"
)

// DefaultEnv is the Worker environment used when a Worker record leaves it blank.
const DefaultEnv = "production"

// Cred is one named Cloudflare API credential (scoped token + account).
type Cred struct {
	Name      string `json:"name"`
	AccountID string `json:"account_id"`
	Token     string `json:"token"`
}

// Worker is a registered Cloudflare Worker: which cred manages it and which
// major category (大类) it draws domains from.
type Worker struct {
	Name     string `json:"name"`               // Cloudflare Worker script name (the "service")
	Cred     string `json:"cred"`               // managing cred name
	Category string `json:"category,omitempty"` // bound 大类
	Env      string `json:"env,omitempty"`      // Worker environment (default production)
}

// Domain is one record-book entry: a hostname under a major category (大类),
// an optional minor category (小类), a status, the worker it is currently
// attached to (when used), plus lifecycle metadata (purchase date, intended
// use, DNS, readiness, last-changed time).
type Domain struct {
	Name     string `json:"name"`
	Category string `json:"category"`      // 大类
	Sub      string `json:"sub,omitempty"` // 小类
	Status   string `json:"status"`
	Worker   string `json:"worker,omitempty"`

	PurchasedAt string `json:"purchased_at,omitempty"` // 购买日期(自由格式,如 2024-01-15)
	Usage       string `json:"usage,omitempty"`        // 准备用在哪里
	DNS         string `json:"dns,omitempty"`          // dns 是什么
	Ready       bool   `json:"ready,omitempty"`        // 是否准备就绪
	ChangedAt   string `json:"changed_at,omitempty"`   // 什么时间更换的(绑定时自动记,也可手改)
}

var (
	nameRe = regexp.MustCompile(`^[a-zA-Z0-9_.-]{1,64}$`)
	hostRe = regexp.MustCompile(`^([a-zA-Z0-9_]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?\.)+[a-zA-Z]{2,63}$`)
)

// NameValid reports whether s is an acceptable cred/worker/category name.
func NameValid(s string) bool { return nameRe.MatchString(s) }

// HostValid reports whether s looks like a domain/hostname.
func HostValid(s string) bool { return hostRe.MatchString(s) }

// NormalizeStatus maps a user-supplied status (English or Chinese) to a
// canonical value, reporting whether it was recognized.
func NormalizeStatus(s string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case StatusUnused, "未使用", "空闲", "free":
		return StatusUnused, true
	case StatusUsed, "已使用", "在用":
		return StatusUsed, true
	case StatusBanned, "被ban", "ban", "封禁":
		return StatusBanned, true
	}
	return "", false
}

type store struct {
	mu        sync.RWMutex
	path      string
	creds     map[string]*Cred
	workers   map[string]*Worker
	domains   map[string]*Domain
	failovers map[string]*FailoverRule
}

var def = &store{
	creds:     map[string]*Cred{},
	workers:   map[string]*Worker{},
	domains:   map[string]*Domain{},
	failovers: map[string]*FailoverRule{},
}

// Init loads persisted state from path (created lazily on first save). It
// starts from an empty store so a re-Init (e.g. between tests) does not retain
// state from a previous path.
func Init(path string) error {
	def.mu.Lock()
	defer def.mu.Unlock()
	def.path = path
	def.creds = map[string]*Cred{}
	def.workers = map[string]*Worker{}
	def.domains = map[string]*Domain{}
	def.failovers = map[string]*FailoverRule{}
	resetFailoverState()
	return def.load()
}

// --- Creds ---

// AddCred registers a credential (fails on duplicate name).
func AddCred(name, accountID, token string) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("凭据名只能是 a-zA-Z0-9_.- 且不超过 64 位")
	}
	if strings.TrimSpace(accountID) == "" || strings.TrimSpace(token) == "" {
		return fmt.Errorf("account_id 和 token 不能为空")
	}
	def.mu.Lock()
	defer def.mu.Unlock()
	if _, ok := def.creds[name]; ok {
		return fmt.Errorf("已存在同名凭据:%s", name)
	}
	def.creds[name] = &Cred{Name: name, AccountID: accountID, Token: token}
	return def.save()
}

// GetCred returns a copy of the named credential.
func GetCred(name string) (*Cred, bool) {
	def.mu.RLock()
	defer def.mu.RUnlock()
	c, ok := def.creds[name]
	if !ok {
		return nil, false
	}
	cp := *c
	return &cp, true
}

// ListCreds returns copies of all credentials, sorted by name.
func ListCreds() []*Cred {
	def.mu.RLock()
	defer def.mu.RUnlock()
	out := make([]*Cred, 0, len(def.creds))
	for _, c := range def.creds {
		cp := *c
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// DelCred removes a credential. It fails if any worker still references it.
func DelCred(name string) error {
	def.mu.Lock()
	defer def.mu.Unlock()
	if _, ok := def.creds[name]; !ok {
		return fmt.Errorf("没有这个凭据:%s", name)
	}
	for _, w := range def.workers {
		if w.Cred == name {
			return fmt.Errorf("凭据 %s 仍被 worker %s 使用,先解绑", name, w.Name)
		}
	}
	delete(def.creds, name)
	return def.save()
}

// --- Workers ---

// AddWorker registers a worker under a cred, with an optional bound category.
func AddWorker(name, cred, category string) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("worker 名只能是 a-zA-Z0-9_.- 且不超过 64 位")
	}
	def.mu.Lock()
	defer def.mu.Unlock()
	if _, ok := def.creds[cred]; !ok {
		return fmt.Errorf("没有这个凭据:%s", cred)
	}
	if _, ok := def.workers[name]; ok {
		return fmt.Errorf("已存在同名 worker:%s", name)
	}
	if category != "" && !nameRe.MatchString(category) {
		return fmt.Errorf("大类名只能是 a-zA-Z0-9_.-")
	}
	def.workers[name] = &Worker{Name: name, Cred: cred, Category: category, Env: DefaultEnv}
	return def.save()
}

// GetWorker returns a copy of the named worker.
func GetWorker(name string) (*Worker, bool) {
	def.mu.RLock()
	defer def.mu.RUnlock()
	w, ok := def.workers[name]
	if !ok {
		return nil, false
	}
	cp := *w
	return &cp, true
}

// ListWorkers returns copies of all workers, sorted by name.
func ListWorkers() []*Worker {
	def.mu.RLock()
	defer def.mu.RUnlock()
	out := make([]*Worker, 0, len(def.workers))
	for _, w := range def.workers {
		cp := *w
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// MutateWorker applies fn to the named worker under lock and persists.
func MutateWorker(name string, fn func(*Worker) error) error {
	def.mu.Lock()
	defer def.mu.Unlock()
	w, ok := def.workers[name]
	if !ok {
		return fmt.Errorf("没有这个 worker:%s", name)
	}
	if err := fn(w); err != nil {
		return err
	}
	return def.save()
}

// DelWorker removes a worker record (does not touch Cloudflare).
func DelWorker(name string) error {
	def.mu.Lock()
	defer def.mu.Unlock()
	if _, ok := def.workers[name]; !ok {
		return fmt.Errorf("没有这个 worker:%s", name)
	}
	delete(def.workers, name)
	return def.save()
}

// --- Domains ---

// AddDomain records a hostname under a category as unused. sub is optional.
func AddDomain(category, name, sub string) error {
	if !nameRe.MatchString(category) {
		return fmt.Errorf("大类名只能是 a-zA-Z0-9_.-")
	}
	if !hostRe.MatchString(name) {
		return fmt.Errorf("域名格式不对:%s", name)
	}
	def.mu.Lock()
	defer def.mu.Unlock()
	if _, ok := def.domains[name]; ok {
		return fmt.Errorf("域名已在记录里:%s", name)
	}
	def.domains[name] = &Domain{Name: name, Category: category, Sub: sub, Status: StatusUnused}
	return def.save()
}

// GetDomain returns a copy of the named domain record.
func GetDomain(name string) (*Domain, bool) {
	def.mu.RLock()
	defer def.mu.RUnlock()
	d, ok := def.domains[name]
	if !ok {
		return nil, false
	}
	cp := *d
	return &cp, true
}

// ListDomains returns copies of domain records filtered by category and/or
// status (empty filter matches all), sorted by category then name.
func ListDomains(category, status string) []*Domain {
	def.mu.RLock()
	defer def.mu.RUnlock()
	out := make([]*Domain, 0, len(def.domains))
	for _, d := range def.domains {
		if category != "" && d.Category != category {
			continue
		}
		if status != "" && d.Status != status {
			continue
		}
		cp := *d
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Category != out[j].Category {
			return out[i].Category < out[j].Category
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// NextUnused returns a copy of the first unused domain in category (sorted by
// name for determinism), or false if the category has none free.
func NextUnused(category string) (*Domain, bool) {
	for _, d := range ListDomains(category, StatusUnused) {
		return d, true
	}
	return nil, false
}

// MutateDomain applies fn to the named domain under lock and persists.
func MutateDomain(name string, fn func(*Domain) error) error {
	def.mu.Lock()
	defer def.mu.Unlock()
	d, ok := def.domains[name]
	if !ok {
		return fmt.Errorf("没有这个域名记录:%s", name)
	}
	if err := fn(d); err != nil {
		return err
	}
	return def.save()
}

// DelDomain removes a domain record (does not touch Cloudflare).
func DelDomain(name string) error {
	def.mu.Lock()
	defer def.mu.Unlock()
	if _, ok := def.domains[name]; !ok {
		return fmt.Errorf("没有这个域名记录:%s", name)
	}
	delete(def.domains, name)
	return def.save()
}

// --- Persistence (JSON); load/save run with the lock held ---

type fileModel struct {
	Creds     []*Cred         `json:"creds"`
	Workers   []*Worker       `json:"workers"`
	Domains   []*Domain       `json:"domains"`
	Failovers []*FailoverRule `json:"failovers,omitempty"`
}

func (s *store) load() error {
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var m fileModel
	if err := sonic.Unmarshal(data, &m); err != nil {
		return fmt.Errorf("解析 %s: %w", s.path, err)
	}
	for _, c := range m.Creds {
		s.creds[c.Name] = c
	}
	for _, w := range m.Workers {
		if w.Env == "" {
			w.Env = DefaultEnv
		}
		s.workers[w.Name] = w
	}
	for _, d := range m.Domains {
		s.domains[d.Name] = d
	}
	for _, f := range m.Failovers {
		if f.Mode == "" {
			f.Mode = FailoverManual
		}
		if f.Threshold <= 0 {
			f.Threshold = DefaultThreshold
		}
		s.failovers[f.Name] = f
	}
	return nil
}

func (s *store) save() error {
	m := fileModel{}
	for _, c := range s.creds {
		m.Creds = append(m.Creds, c)
	}
	for _, w := range s.workers {
		m.Workers = append(m.Workers, w)
	}
	for _, d := range s.domains {
		m.Domains = append(m.Domains, d)
	}
	for _, f := range s.failovers {
		m.Failovers = append(m.Failovers, f)
	}
	sort.Slice(m.Creds, func(i, j int) bool { return m.Creds[i].Name < m.Creds[j].Name })
	sort.Slice(m.Workers, func(i, j int) bool { return m.Workers[i].Name < m.Workers[j].Name })
	sort.Slice(m.Domains, func(i, j int) bool { return m.Domains[i].Name < m.Domains[j].Name })
	sort.Slice(m.Failovers, func(i, j int) bool { return m.Failovers[i].Name < m.Failovers[j].Name })
	data, err := sonic.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0o600)
}
