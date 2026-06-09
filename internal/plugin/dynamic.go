package plugin

import (
	"fmt"
	"log"
	"sort"
	"sync"

	tele "gopkg.in/telebot.v3"
)

// Dynamic commands are registered at runtime (e.g. one command per
// playground group) and merged into the Telegram command menu alongside
// the static plugin commands.
var (
	cmdMu    sync.Mutex
	baseMenu []tele.Command              // static commands, frozen by Setup
	dynamic  = map[string]tele.Command{} // live dynamic commands, keyed by name
	menuLive bool                        // true once Setup pushed the initial menu
)

// AddDynamic registers a runtime command handler and refreshes the Telegram
// command menu. Names that collide with a static command are refused.
// Calls made during Setup (i.e. from a Wirer) are batched into the initial
// menu push instead of triggering one API call each.
func AddDynamic(b *tele.Bot, name, desc string, h tele.HandlerFunc, mw ...tele.MiddlewareFunc) error {
	cmdMu.Lock()
	for _, c := range baseMenu {
		if c.Text == name {
			cmdMu.Unlock()
			return fmt.Errorf("命令 /%s 与内置命令冲突", name)
		}
	}
	dynamic[name] = tele.Command{Text: name, Description: desc}
	cmdMu.Unlock()

	b.Handle("/"+name, h, mw...)
	refreshMenu(b)
	return nil
}

// RemoveDynamic drops a dynamic command from the menu. telebot cannot
// unregister handlers, so the handler itself must tolerate being invoked
// for a name that no longer exists.
func RemoveDynamic(b *tele.Bot, name string) {
	cmdMu.Lock()
	delete(dynamic, name)
	cmdMu.Unlock()
	refreshMenu(b)
}

// fullMenu returns static commands followed by dynamic ones (sorted by name).
func fullMenu() []tele.Command {
	cmdMu.Lock()
	defer cmdMu.Unlock()
	out := append([]tele.Command(nil), baseMenu...)
	names := make([]string, 0, len(dynamic))
	for n := range dynamic {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		out = append(out, dynamic[n])
	}
	return out
}

// refreshMenu pushes the merged menu via setMyCommands. No-op until Setup
// has pushed the initial menu.
func refreshMenu(b *tele.Bot) {
	cmdMu.Lock()
	live := menuLive
	cmdMu.Unlock()
	if !live {
		return
	}
	if err := b.SetCommands(fullMenu()); err != nil {
		log.Printf("[menu] 刷新命令菜单失败: %v", err)
	}
}
