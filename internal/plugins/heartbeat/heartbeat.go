// Package heartbeat demonstrates a pure scheduled-job plugin (no commands,
// only a cron job).
package heartbeat

import (
	"log"
	"time"

	tele "gopkg.in/telebot.v3"

	"github.com/moyoez/magibox/internal/auth"
	"github.com/moyoez/magibox/internal/plugin"
)

type Heartbeat struct{ plugin.Base }

func (Heartbeat) Name() string { return "heartbeat" }

func (Heartbeat) Jobs() []plugin.Job {
	return []plugin.Job{{
		Name: "heartbeat",
		// Demo: once per minute. In production switch to a standard cron spec,
		// e.g. "0 9 * * *" (daily at 09:00).
		Spec: "@every 1m",
		Run: func(b *tele.Bot) {
			msg := "💓 心跳 " + time.Now().Format("2006-01-02 15:04:05")
			ids := auth.IDs(auth.RoleAdmin) // push to admin and above
			if len(ids) == 0 {
				log.Println("[heartbeat]", msg, "(暂无管理员,仅打印;发 /bind 绑定 owner)")
				return
			}
			for _, id := range ids {
				if _, err := b.Send(tele.ChatID(id), msg); err != nil {
					log.Printf("[heartbeat] 发送给 %d 失败: %v", id, err)
				}
			}
		},
	}}
}

func init() { plugin.Register(Heartbeat{}) }
