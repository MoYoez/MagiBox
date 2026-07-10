// Package magibox provides the reusable host for public and external plugins.
package magibox

import (
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/robfig/cron/v3"
	tele "gopkg.in/telebot.v3"

	"github.com/moyoez/magibox/internal/auth"
	"github.com/moyoez/magibox/internal/bundle"
	"github.com/moyoez/magibox/internal/config"
	"github.com/moyoez/magibox/internal/playground"
	_ "github.com/moyoez/magibox/internal/plugins"
	"github.com/moyoez/magibox/internal/uptime"
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
	if err := uptime.Init(config.UptimeStorePath()); err != nil {
		return fmt.Errorf("初始化 uptime: %w", err)
	}

	// Filter is applied before building HTTP routes so plugin.HTTPRoutes
	// honors disabled plugins.
	plugin.SetFilter(config.PluginsMode(), config.PluginsList())

	// Shared HTTP server: plugin routes (e.g. inbound webhooks) mount first,
	// then bundle handles everything else (/b/, /m/). The listener is opened
	// and serving before plugin.Setup so a port conflict surfaces first and the
	// listener is torn down if Setup fails (see run_test.go).
	addr := config.BundleAddr()
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("启动 HTTP 服务 %s: %w", addr, err)
	}
	defer listener.Close()

	mux := http.NewServeMux()
	for _, rt := range plugin.HTTPRoutes(b) {
		mux.Handle(rt.Pattern, rt.Handler)
		log.Printf("[http] 挂载路由 %s", rt.Pattern)
	}
	mux.Handle("/", bundle.Handler())

	log.Printf("HTTP 服务监听 %s(base=%s)", addr, config.BundleBaseURL())
	go func() {
		if err := http.Serve(listener, mux); err != nil && !errors.Is(err, net.ErrClosed) {
			log.Printf("[http] 服务退出: %v", err)
		}
	}()

	c := cron.New()
	if err := plugin.Setup(b, c); err != nil {
		return fmt.Errorf("设置插件: %w", err)
	}
	c.Start()
	defer c.Stop()

	log.Println("bot 已启动,Ctrl+C 退出")
	b.Start()
	return nil
}
