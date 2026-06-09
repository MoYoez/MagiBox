// Command bot starts the Telegram bot: it wires up all plugins (commands + cron jobs) and runs.
package main

import (
	"log"
	"time"

	"github.com/robfig/cron/v3"
	tele "gopkg.in/telebot.v3"

	"github.com/moyoez/magibox/internal/auth"
	"github.com/moyoez/magibox/internal/bundle"
	"github.com/moyoez/magibox/internal/config"
	"github.com/moyoez/magibox/internal/playground"
	"github.com/moyoez/magibox/internal/plugin"

	_ "github.com/moyoez/magibox/internal/plugins" // trigger self-registration of all plugins via init()
)

func main() {
	token := config.Token()
	if token == "" {
		log.Fatal("BOT_TOKEN 未设置(见 .env.example)")
	}

	b, err := tele.NewBot(tele.Settings{
		Token:  token,
		Poller: &tele.LongPoller{Timeout: 10 * time.Second},
		OnError: func(err error, c tele.Context) {
			log.Printf("[handler error] %v", err)
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	// Sync optional profile fields (name / description / about) to Telegram,
	// mirroring BotFather's /setname, /setdescription and /setabouttext.
	plugin.SyncProfile(b, config.BotName(), config.BotDescription(), config.BotAbout())

	// Load role permissions; if there is no owner yet, a one-time pairing code is printed to the terminal.
	if err := auth.Init(config.AuthStorePath()); err != nil {
		log.Fatal(err)
	}
	// Load playground group config and variable table.
	if err := playground.Init(config.PlaygroundStorePath()); err != nil {
		log.Fatal(err)
	}
	if err := playground.InitVars(config.VarsStorePath()); err != nil {
		log.Fatal(err)
	}
	// Load chat bundles and start the HTTP server (serves bundle URLs).
	if err := bundle.Init(config.BundleStorePath(), config.BundleMediaDir(), config.BundleBaseURL()); err != nil {
		log.Fatal(err)
	}
	go func() {
		addr := config.BundleAddr()
		log.Printf("bundle HTTP 服务监听 %s(base=%s)", addr, config.BundleBaseURL())
		if err := bundle.Serve(addr); err != nil {
			log.Printf("[bundle] HTTP 服务退出: %v", err)
		}
	}()

	c := cron.New()
	// Apply the plugin on/off filter (blacklist or whitelist) before wiring.
	plugin.SetFilter(config.PluginsMode(), config.PluginsList())
	if err := plugin.Setup(b, c); err != nil {
		log.Fatal(err)
	}
	c.Start()
	defer c.Stop()

	log.Println("bot 已启动,Ctrl+C 退出")
	b.Start() // blocks until the process is interrupted
}
