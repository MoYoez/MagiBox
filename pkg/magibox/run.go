// Package magibox provides the reusable host for public and external plugins.
package magibox

import (
	"fmt"
	"log"
	"time"

	"github.com/robfig/cron/v3"
	tele "gopkg.in/telebot.v3"

	"github.com/moyoez/magibox/internal/auth"
	"github.com/moyoez/magibox/internal/bundle"
	"github.com/moyoez/magibox/internal/config"
	"github.com/moyoez/magibox/internal/playground"
	_ "github.com/moyoez/magibox/internal/plugins"
	"github.com/moyoez/magibox/pkg/plugin"
)

// Run initializes MagiBox and blocks until the Telegram bot stops.
// External host binaries register their plugins through init before calling Run.
func Run() error {
	token := config.Token()
	if token == "" {
		return fmt.Errorf("BOT_TOKEN 未设置(见 .env.example)")
	}

	b, err := tele.NewBot(tele.Settings{
		Token:  token,
		Poller: &tele.LongPoller{Timeout: 10 * time.Second},
		OnError: func(err error, c tele.Context) {
			log.Printf("[handler error] %v", err)
		},
	})
	if err != nil {
		return fmt.Errorf("创建 Telegram Bot: %w", err)
	}

	plugin.SyncProfile(b, config.BotName(), config.BotDescription(), config.BotAbout())
	if err := auth.Init(config.AuthStorePath()); err != nil {
		return fmt.Errorf("初始化权限: %w", err)
	}
	if err := playground.Init(config.PlaygroundStorePath()); err != nil {
		return fmt.Errorf("初始化 playground: %w", err)
	}
	if err := playground.InitVars(config.VarsStorePath()); err != nil {
		return fmt.Errorf("初始化变量表: %w", err)
	}
	if err := bundle.Init(config.BundleStorePath(), config.BundleMediaDir(), config.BundleBaseURL()); err != nil {
		return fmt.Errorf("初始化 bundle: %w", err)
	}

	go func() {
		addr := config.BundleAddr()
		log.Printf("bundle HTTP 服务监听 %s(base=%s)", addr, config.BundleBaseURL())
		if err := bundle.Serve(addr); err != nil {
			log.Printf("[bundle] HTTP 服务退出: %v", err)
		}
	}()

	c := cron.New()
	plugin.SetFilter(config.PluginsMode(), config.PluginsList())
	if err := plugin.Setup(b, c); err != nil {
		return fmt.Errorf("设置插件: %w", err)
	}
	c.Start()
	defer c.Stop()

	log.Println("bot 已启动,Ctrl+C 退出")
	b.Start()
	return nil
}
