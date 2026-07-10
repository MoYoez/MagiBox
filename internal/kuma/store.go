// Package kuma is the core of the /kuma plugin: it stores Uptime Kuma REST
// wrapper credentials (base URL + optional API key) and creates/lists/deletes
// monitors through that wrapper's HTTP API.
//
// It targets the keithah/uptime-kuma-rest-api shape (POST/GET /monitors,
// DELETE /monitors/{id}); the wrapper itself holds the Kuma login, so the API
// key is only sent (as a Bearer token) when the wrapper is fronted by one.
//
// It carries no Telegram dependency; the plugin glue in internal/plugins/kuma
// drives it from commands.
package kuma

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/bytedance/sonic"
)

// Cred is one named Uptime Kuma REST wrapper endpoint.
type Cred struct {
	Name    string `json:"name"`
	BaseURL string `json:"base_url"`
	APIKey  string `json:"api_key,omitempty"`
}

var nameRe = regexp.MustCompile(`^[a-zA-Z0-9_.-]{1,64}$`)

// NameValid reports whether s is an acceptable cred name.
func NameValid(s string) bool { return nameRe.MatchString(s) }

type store struct {
	mu    sync.RWMutex
	path  string
	creds map[string]*Cred
}

var def = &store{creds: map[string]*Cred{}}

// Init loads persisted creds from path (created lazily on first save).
func Init(path string) error {
	def.mu.Lock()
	defer def.mu.Unlock()
	def.path = path
	return def.load()
}

// AddCred registers a Kuma wrapper credential (fails on duplicate name).
func AddCred(name, baseURL, apiKey string) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("凭据名只能是 a-zA-Z0-9_.- 且不超过 64 位")
	}
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if !strings.HasPrefix(baseURL, "http://") && !strings.HasPrefix(baseURL, "https://") {
		return fmt.Errorf("base_url 需以 http:// 或 https:// 开头")
	}
	def.mu.Lock()
	defer def.mu.Unlock()
	if _, ok := def.creds[name]; ok {
		return fmt.Errorf("已存在同名凭据:%s", name)
	}
	def.creds[name] = &Cred{Name: name, BaseURL: baseURL, APIKey: strings.TrimSpace(apiKey)}
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

// DelCred removes a credential.
func DelCred(name string) error {
	def.mu.Lock()
	defer def.mu.Unlock()
	if _, ok := def.creds[name]; !ok {
		return fmt.Errorf("没有这个凭据:%s", name)
	}
	delete(def.creds, name)
	return def.save()
}

// --- Persistence (JSON); load/save run with the lock held ---

type fileModel struct {
	Creds []*Cred `json:"creds"`
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
	return nil
}

func (s *store) save() error {
	m := fileModel{}
	for _, c := range s.creds {
		m.Creds = append(m.Creds, c)
	}
	sort.Slice(m.Creds, func(i, j int) bool { return m.Creds[i].Name < m.Creds[j].Name })
	data, err := sonic.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0o600)
}
