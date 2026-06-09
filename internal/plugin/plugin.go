// Package plugin defines the plugin contract and the global registry.
//
// Each feature = one package implementing Plugin that calls Register in
// its own init(). At startup (Setup) the framework collects everything:
// commands are mounted automatically and pushed to Telegram, and scheduled
// jobs are registered with cron. Adding a feature only takes a new package
// + one blank import, with zero changes to main or other plugins.
package plugin

import tele "gopkg.in/telebot.v3"

// Plugin is a self-contained feature unit.
// Embed Base to implement only the methods you need (Name must be implemented yourself).
type Plugin interface {
	Name() string        // plugin identifier (for logging / debugging)
	Commands() []Command // declared commands (may be empty)
	Jobs() []Job         // declared scheduled jobs (may be empty)
}

// Command is the full declaration of a single / command.
type Command struct {
	Name        string                // command name without the leading / (e.g. "echo")
	Description string                // shown in the Telegram command menu
	Handler     tele.HandlerFunc      // handler function
	Middleware  []tele.MiddlewareFunc // command-specific middleware (e.g. AdminOnly), may be empty
}

// Job is a cron scheduled task.
type Job struct {
	Name string            // job name (for logging)
	Spec string            // cron expression or descriptor: "0 9 * * *" / "@every 1m" / "@daily"
	Run  func(b *tele.Bot) // executed when due; pushes messages proactively via b
}

// Base provides empty default implementations of Commands/Jobs for plugins to embed as needed.
type Base struct{}

func (Base) Commands() []Command { return nil }
func (Base) Jobs() []Job         { return nil }

// registry holds all plugins registered in this process.
var registry []Plugin

// Register adds a plugin to the registry, typically called from the plugin package's init().
func Register(p Plugin) { registry = append(registry, p) }

// All returns all registered plugins in registration order.
func All() []Plugin { return registry }
