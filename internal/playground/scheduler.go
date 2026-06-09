package playground

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/robfig/cron/v3"
)

// CheckEvent is the event type a health check produces after debouncing.
type CheckEvent int

const (
	EventSilent  CheckEvent = iota // no notification (failures below threshold / still failing / still healthy)
	EventReport                    // no assertions: periodically report the rendered result
	EventFail                      // assertions failed and threshold reached for the first time: alert
	EventRecover                   // recovered from the alerting state
)

// ResultFunc is the health-check result callback (resp is nil when the request failed).
type ResultFunc func(g *Group, ev CheckEvent, resp *Response, failures []string)

type checkState struct {
	fails    int
	alerting bool
}

// StartScheduler injects the cron instance and result callback, and registers
// health checks for groups that already have a schedule. Called once by the
// playground plugin during wiring.
func StartScheduler(c *cron.Cron, onResult ResultFunc) {
	def.mu.Lock()
	defer def.mu.Unlock()
	def.cron = c
	def.onResult = onResult
	if def.entries == nil {
		def.entries = map[string]cron.EntryID{}
	}
	for name, g := range def.groups {
		if g.Schedule != "" {
			def.scheduleLocked(name, g.Schedule)
		}
	}
}

// SetSchedule sets (or disables, with an empty string) a group's health-check
// schedule, effective immediately. args are the runtime arguments used when
// the check runs (equivalent to /pg run key=value, highest precedence);
// disabling the schedule also clears args.
func SetSchedule(name, spec string, args map[string]string) error {
	def.mu.Lock()
	defer def.mu.Unlock()
	g, ok := def.groups[name]
	if !ok {
		return fmt.Errorf("组 %q 不存在", name)
	}
	if spec != "" {
		if _, err := cron.ParseStandard(spec); err != nil {
			return fmt.Errorf("cron 表达式非法: %w", err)
		}
	}
	g.Schedule = spec
	if spec == "" || len(args) == 0 {
		g.ScheduleArgs = nil
	} else {
		g.ScheduleArgs = args
	}
	def.scheduleLocked(name, spec)
	return def.save()
}

// scheduleLocked adds/removes a group's cron entry while the lock is held.
func (s *store) scheduleLocked(name, spec string) {
	if s.cron == nil {
		return // scheduler not started yet (e.g. unit tests); the schedule field is saved and registered on startup
	}
	if id, ok := s.entries[name]; ok {
		s.cron.Remove(id)
		delete(s.entries, name)
	}
	if spec == "" {
		return
	}
	id, err := s.cron.AddFunc(spec, func() { runCheck(name) })
	if err != nil {
		log.Printf("[playground] 组 %s schedule %q 非法: %v", name, spec, err)
		return
	}
	s.entries[name] = id
}

// classify advances a group's debounce state machine and returns the event to fire.
func (s *store) classify(name string, g *Group, failures []string) CheckEvent {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(g.Asserts) == 0 {
		return EventReport // no assertions: report mode, no debouncing involved
	}
	if s.states == nil {
		s.states = map[string]*checkState{}
	}
	st := s.states[name]
	if st == nil {
		st = &checkState{}
		s.states[name] = st
	}
	threshold := g.FailThreshold
	if threshold < 1 {
		threshold = 1
	}
	if len(failures) > 0 {
		st.fails++
		if st.fails >= threshold && !st.alerting {
			st.alerting = true
			return EventFail
		}
		return EventSilent
	}
	// success
	st.fails = 0
	if st.alerting {
		st.alerting = false
		return EventRecover
	}
	return EventSilent
}

// runCheck performs one health check and hands the result to the callback
// (runs in a cron goroutine).
func runCheck(name string) {
	g, ok := Get(name)
	if !ok {
		return
	}
	def.mu.RLock()
	cb := def.onResult
	def.mu.RUnlock()
	if cb == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// Checks carry the schedule-level args (configured via /pg sched),
	// equivalent to a manual /pg run <group> key=value.
	resp, err := Execute(ctx, g, g.ScheduleArgs)
	var failures []string
	if err != nil {
		failures = []string{"请求失败: " + err.Error()}
	} else {
		failures = EvalAsserts(g.Asserts, resp)
	}

	ev := def.classify(name, g, failures)
	if ev == EventSilent {
		return
	}
	cb(g, ev, resp, failures)
}
