package plugins_test

import (
	"testing"

	"github.com/robfig/cron/v3"

	"github.com/moyoez/magibox/internal/plugin"
	_ "github.com/moyoez/magibox/internal/plugins" // trigger all plugins' init() self-registration
)

// Plugins should auto-register into the registry via init() at import time.
func TestPluginsSelfRegister(t *testing.T) {
	if n := len(plugin.All()); n != 6 {
		t.Fatalf("已注册插件 = %d,期望 6(echo/ping/bind/perm/playground/bundle)", n)
	}
}

// Commands declared by each plugin should be collected by the framework.
func TestCommandsCollected(t *testing.T) {
	want := map[string]bool{
		"echo": false, "ping": false, "whoami": false,
		"bind": false, "members": false, "promote": false, "demote": false,
		"pg": false, "bundle": false,
		// standalone /pg_* subcommands
		"pg_run": false, "pg_sched": false, "pg_new": false, "pg_var": false,
	}
	for _, p := range plugin.All() {
		for _, cmd := range p.Commands() {
			if _, ok := want[cmd.Name]; ok {
				want[cmd.Name] = true
			}
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("命令 /%s 未被收集", name)
		}
	}
}

// Every scheduled job's cron spec should be accepted by the scheduler
// (without Start, nothing actually runs).
func TestJobSpecsValid(t *testing.T) {
	c := cron.New()
	for _, p := range plugin.All() {
		for _, j := range p.Jobs() {
			if _, err := c.AddFunc(j.Spec, func() {}); err != nil {
				t.Errorf("插件 %s 的任务 %q spec 非法 (%s): %v", p.Name(), j.Name, j.Spec, err)
			}
		}
	}
}
