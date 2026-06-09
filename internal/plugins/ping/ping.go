// Package ping demonstrates a public command plugin.
package ping

import (
	"fmt"
	"strings"

	tele "gopkg.in/telebot.v3"

	"github.com/moyoez/magibox/internal/auth"
	"github.com/moyoez/magibox/internal/plugin"
)

type Ping struct{ plugin.Base }

func (Ping) Name() string { return "ping" }

func (Ping) Commands() []plugin.Command {
	return []plugin.Command{
		{
			Name:        "ping",
			Description: "测试 bot 是否存活",
			Handler: func(c tele.Context) error {
				return c.Send("🏓 pong")
			},
		},
		{
			Name:        "whoami",
			Description: "显示 chat id / user id 和角色(群里含群 id;回复某人可看其 id)",
			Handler:     handleWhoami,
		},
	}
}

// handleWhoami shows the ids and roles of the current chat and the sender:
// in a private chat the two are the same so only one is shown; in a group it
// lists the group chat id and your user id separately; when replying to a
// message it also shows the replied-to user's id (handy for /promote).
func handleWhoami(c tele.Context) error {
	var sb strings.Builder
	chatID := c.Chat().ID
	uid := c.Sender().ID
	if chatID == uid { // private chat
		fmt.Fprintf(&sb, "chat id: %d\n角色: %s", uid, auth.RoleOf(uid))
	} else { // group / channel
		fmt.Fprintf(&sb, "本群 chat id: %d(角色: %s)\n", chatID, auth.RoleOf(chatID))
		fmt.Fprintf(&sb, "你的 user id: %d(角色: %s)", uid, auth.RoleOf(uid))
	}
	if r := c.Message().ReplyTo; r != nil && r.Sender != nil {
		fmt.Fprintf(&sb, "\n被回复者 user id: %d(角色: %s)", r.Sender.ID, auth.RoleOf(r.Sender.ID))
	}
	return c.Send(sb.String())
}

func init() { plugin.Register(Ping{}) }
