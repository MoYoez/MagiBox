// Package playground provides configurable HTTP endpoint groups: each group
// stores a BaseURL / endpoint / method / headers plus a programmable response
// template; after execution the response is rendered through the template.
// A group may also carry a health-check schedule (cron) + assertions that run
// automatically on schedule (see scheduler.go).
package playground

import (
	"fmt"
	"os"
	"sort"
	"sync"

	"github.com/bytedance/sonic"
	"github.com/robfig/cron/v3"
)

// Group is the complete configuration of an endpoint group.
type Group struct {
	Name          string            `json:"name"`
	BaseURL       string            `json:"base_url"`
	Endpoint      string            `json:"endpoint"`
	Method        string            `json:"method"`
	Headers       map[string]string `json:"headers"`
	Body          string            `json:"body,omitempty"`
	Template      string            `json:"template"`
	Schedule      string            `json:"schedule,omitempty"`       // cron expression for health checks; empty = no checks
	ScheduleArgs  map[string]string `json:"schedule_args,omitempty"`  // runtime args used when a scheduled check runs (same as /pg run key=value)
	Asserts       []Assert          `json:"asserts,omitempty"`        // assertion list (AND)
	FailThreshold int               `json:"fail_threshold,omitempty"` // consecutive failures before alerting (default 1)
}

func (g *Group) clone() *Group {
	cp := *g
	cp.Headers = make(map[string]string, len(g.Headers))
	for k, v := range g.Headers {
		cp.Headers[k] = v
	}
	if g.ScheduleArgs != nil {
		cp.ScheduleArgs = make(map[string]string, len(g.ScheduleArgs))
		for k, v := range g.ScheduleArgs {
			cp.ScheduleArgs[k] = v
		}
	}
	if g.Asserts != nil {
		cp.Asserts = append([]Assert(nil), g.Asserts...)
	}
	return &cp
}

type store struct {
	mu       sync.RWMutex
	path     string
	groups   map[string]*Group
	cron     *cron.Cron
	entries  map[string]cron.EntryID
	onResult ResultFunc
	states   map[string]*checkState
}

var def = &store{groups: map[string]*Group{}, entries: map[string]cron.EntryID{}, states: map[string]*checkState{}}

// Init loads the persisted group configs (and resets in-memory state).
func Init(path string) error {
	def.mu.Lock()
	defer def.mu.Unlock()
	def.path = path
	def.groups = map[string]*Group{}
	def.entries = map[string]cron.EntryID{}
	def.states = map[string]*checkState{}
	return def.load()
}

// Create makes a new empty group (default GET + default template).
func Create(name string) error {
	def.mu.Lock()
	defer def.mu.Unlock()
	if name == "" {
		return fmt.Errorf("组名不能为空")
	}
	if _, ok := def.groups[name]; ok {
		return fmt.Errorf("组 %q 已存在", name)
	}
	def.groups[name] = &Group{
		Name:     name,
		Method:   "GET",
		Headers:  map[string]string{},
		Template: "{body_code}\n{body_raw}",
	}
	return def.save()
}

// Get returns a copy of the group.
func Get(name string) (*Group, bool) {
	def.mu.RLock()
	defer def.mu.RUnlock()
	g, ok := def.groups[name]
	if !ok {
		return nil, false
	}
	return g.clone(), true
}

// List returns copies of all groups, sorted by name.
func List() []*Group {
	def.mu.RLock()
	defer def.mu.RUnlock()
	out := make([]*Group, 0, len(def.groups))
	for _, g := range def.groups {
		out = append(out, g.clone())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Delete removes a group (and unregisters its health check).
func Delete(name string) error {
	def.mu.Lock()
	defer def.mu.Unlock()
	if _, ok := def.groups[name]; !ok {
		return fmt.Errorf("组 %q 不存在", name)
	}
	def.scheduleLocked(name, "") // remove any existing cron entry
	delete(def.groups, name)
	delete(def.states, name)
	return def.save()
}

// Mutate modifies a group under the lock and persists the change.
func Mutate(name string, fn func(*Group) error) error {
	def.mu.Lock()
	defer def.mu.Unlock()
	g, ok := def.groups[name]
	if !ok {
		return fmt.Errorf("组 %q 不存在", name)
	}
	if err := fn(g); err != nil {
		return err
	}
	return def.save()
}

func (s *store) load() error {
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var gs []*Group
	if err := sonic.Unmarshal(data, &gs); err != nil {
		return fmt.Errorf("解析 %s: %w", s.path, err)
	}
	for _, g := range gs {
		if g.Headers == nil {
			g.Headers = map[string]string{}
		}
		s.groups[g.Name] = g
	}
	return nil
}

func (s *store) save() error {
	gs := make([]*Group, 0, len(s.groups))
	for _, g := range s.groups {
		gs = append(gs, g)
	}
	sort.Slice(gs, func(i, j int) bool { return gs[i].Name < gs[j].Name })
	data, err := sonic.MarshalIndent(gs, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0o600)
}
