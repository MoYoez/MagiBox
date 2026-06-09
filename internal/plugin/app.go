package plugin

import (
	"fmt"
	"log"
	"strings"

	"github.com/robfig/cron/v3"
	tele "gopkg.in/telebot.v3"
)

// Scheduler is an optional plugin extension: it receives the bot and cron
// at setup time to wire up "dynamic" scheduled tasks (unlike the static
// tasks from Jobs(), these can be added/removed at runtime).
type Scheduler interface {
	StartSchedule(b *tele.Bot, c *cron.Cron)
}

// Wirer is an optional plugin extension: it receives the bot at setup time
// to register non-command handlers (e.g. the tele.OnText message stream).
type Wirer interface {
	Wire(b *tele.Bot)
}

// Setup wires all registered plugins onto the bot and cron:
//  1. install global middleware (Recover → Logger);
//  2. mount each plugin's command handlers (with per-command middleware);
//  3. register each plugin's scheduled tasks with cron (static Jobs + optional Scheduler);
//  4. auto-generate /help;
//  5. push the command list to Telegram in one call (setMyCommands).
//
// The caller is responsible for calling cron.Start() and bot.Start() afterwards.
func Setup(b *tele.Bot, c *cron.Cron) error {
	b.Use(Recover(), Logger())

	var menu []tele.Command

	for _, p := range All() {
		for _, cmd := range p.Commands() {
			b.Handle("/"+cmd.Name, cmd.Handler, cmd.Middleware...)
			menu = append(menu, tele.Command{Text: cmd.Name, Description: cmd.Description})
			log.Printf("[register] /%s  <- %s", cmd.Name, p.Name())
		}
		for _, job := range p.Jobs() {
			j := job // capture loop variable
			id, err := c.AddFunc(j.Spec, func() { j.Run(b) })
			if err != nil {
				return fmt.Errorf("plugin %s: job %q: %w", p.Name(), j.Name, err)
			}
			log.Printf("[schedule] %s (%s) entry=%d  <- %s", j.Name, j.Spec, id, p.Name())
		}
		if s, ok := p.(Scheduler); ok {
			s.StartSchedule(b, c)
			log.Printf("[schedule] 动态调度器已启动  <- %s", p.Name())
		}
	}

	// Auto-generated /help (added last so it appears in the list itself).
	menu = append(menu, tele.Command{Text: "help", Description: "显示所有命令"})
	help := buildHelp(menu)
	b.Handle("/help", func(c tele.Context) error {
		return c.Send(help)
	})

	// Freeze the static menu before wiring: Wirers may register dynamic
	// commands (AddDynamic), which need the base menu for conflict checks.
	cmdMu.Lock()
	baseMenu = menu
	cmdMu.Unlock()

	for _, p := range All() {
		if w, ok := p.(Wirer); ok {
			w.Wire(b)
			log.Printf("[wire] 已接线消息 handler  <- %s", p.Name())
		}
	}

	// Initial menu push: static commands plus any dynamics added during Wire.
	full := fullMenu()
	if err := b.SetCommands(full); err != nil {
		return fmt.Errorf("set commands: %w", err)
	}
	cmdMu.Lock()
	menuLive = true
	cmdMu.Unlock()
	log.Printf("[ready] %d 条命令已注册到 Telegram", len(full))
	return nil
}

func buildHelp(cmds []tele.Command) string {
	var sb strings.Builder
	sb.WriteString("可用命令:\n")
	for _, c := range cmds {
		fmt.Fprintf(&sb, "/%s — %s\n", c.Text, c.Description)
	}
	return sb.String()
}
