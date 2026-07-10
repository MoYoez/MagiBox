// Package uptime is the core of the uptime webhook watcher: it stores named
// watchers (each with an unguessable callback token, a target chat, and a
// field/template spec) and turns an inbound webhook body into a message.
//
// It carries no Telegram dependency; the plugin glue in
// internal/plugins/uptime mounts the HTTP route and pushes the rendered text.
package uptime

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"regexp"
	"sort"
	"sync"

	"github.com/bytedance/sonic"
)

// Watcher is one webhook endpoint: an inbound callback that formats the
// received JSON and pushes it to a target chat.
type Watcher struct {
	Name     string   `json:"name"`     // identifier, matches nameRe
	Token    string   `json:"token"`    // secret path segment of the callback URL
	Target   int64    `json:"target"`   // chat id to notify (0 = not set yet)
	Fields   []string `json:"fields"`   // JSON dot-paths to extract, e.g. ["ipgroup","isbanned"]
	Template string   `json:"template"` // optional custom template ({path} placeholders); overrides Fields
}

// nameRe restricts watcher names to what reads cleanly in a command.
var nameRe = regexp.MustCompile(`^[a-z0-9_]{1,32}$`)

// NameValid reports whether name is an acceptable watcher name.
func NameValid(name string) bool { return nameRe.MatchString(name) }

type store struct {
	mu       sync.RWMutex
	path     string
	watchers map[string]*Watcher // by name
	byToken  map[string]string   // token -> name
}

var def = &store{watchers: map[string]*Watcher{}, byToken: map[string]string{}}

// Init loads persisted watchers from path (created lazily on first save).
func Init(path string) error {
	def.mu.Lock()
	defer def.mu.Unlock()
	def.path = path
	return def.load()
}

// Create adds a new watcher with a freshly generated callback token.
func Create(name string) (*Watcher, error) {
	if !nameRe.MatchString(name) {
		return nil, fmt.Errorf("名字只能是 a-z0-9_ 且不超过 32 位")
	}
	def.mu.Lock()
	defer def.mu.Unlock()
	if _, ok := def.watchers[name]; ok {
		return nil, fmt.Errorf("已存在同名监听:%s", name)
	}
	w := &Watcher{Name: name, Token: newToken()}
	def.watchers[name] = w
	def.byToken[w.Token] = name
	if err := def.save(); err != nil {
		return nil, err
	}
	return clone(w), nil
}

// Get returns a copy of the named watcher.
func Get(name string) (*Watcher, bool) {
	def.mu.RLock()
	defer def.mu.RUnlock()
	w, ok := def.watchers[name]
	if !ok {
		return nil, false
	}
	return clone(w), true
}

// ByToken returns a copy of the watcher owning token (the callback lookup).
func ByToken(token string) (*Watcher, bool) {
	def.mu.RLock()
	defer def.mu.RUnlock()
	name, ok := def.byToken[token]
	if !ok {
		return nil, false
	}
	return clone(def.watchers[name]), true
}

// List returns copies of all watchers, sorted by name.
func List() []*Watcher {
	def.mu.RLock()
	defer def.mu.RUnlock()
	out := make([]*Watcher, 0, len(def.watchers))
	for _, w := range def.watchers {
		out = append(out, clone(w))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Delete removes a watcher and its token index entry.
func Delete(name string) error {
	def.mu.Lock()
	defer def.mu.Unlock()
	w, ok := def.watchers[name]
	if !ok {
		return fmt.Errorf("没有这个监听:%s", name)
	}
	delete(def.byToken, w.Token)
	delete(def.watchers, name)
	return def.save()
}

// Mutate applies fn to the named watcher under lock and persists the result.
// The token is never mutated here, so the byToken index stays valid.
func Mutate(name string, fn func(*Watcher) error) error {
	def.mu.Lock()
	defer def.mu.Unlock()
	w, ok := def.watchers[name]
	if !ok {
		return fmt.Errorf("没有这个监听:%s", name)
	}
	if err := fn(w); err != nil {
		return err
	}
	return def.save()
}

func clone(w *Watcher) *Watcher {
	c := *w
	if w.Fields != nil {
		c.Fields = append([]string(nil), w.Fields...)
	}
	return &c
}

func newToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// --- Persistence (JSON); load/save run with the lock held ---

type fileModel struct {
	Watchers []*Watcher `json:"watchers"`
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
	for _, w := range m.Watchers {
		def.watchers[w.Name] = w
		def.byToken[w.Token] = w.Name
	}
	return nil
}

func (s *store) save() error {
	m := fileModel{}
	for _, w := range s.watchers {
		m.Watchers = append(m.Watchers, w)
	}
	sort.Slice(m.Watchers, func(i, j int) bool { return m.Watchers[i].Name < m.Watchers[j].Name })
	data, err := sonic.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0o600)
}
