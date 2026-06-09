// Package perm provides permission-management commands: list / promote /
// demote user roles.
package perm

import (
	"fmt"
	"strconv"
	"strings"

	tele "gopkg.in/telebot.v3"

	"github.com/moyoez/magibox/internal/auth"
	"github.com/moyoez/magibox/internal/plugin"
)

type Perm struct{ plugin.Base }

func (Perm) Name() string { return "perm" }

func (Perm) Commands() []plugin.Command {
	return []plugin.Command{
		{
			Name:        "members",
			Description: "列出所有 admin / owner(需 admin)",
			Middleware:  []tele.MiddlewareFunc{auth.RequireAdmin()},
			Handler:     handleMembers,
		},
		{
			Name:        "promote",
			Description: "提升为 admin:/promote <chat_id> 或回复某人(需 owner)",
			Middleware:  []tele.MiddlewareFunc{auth.RequireOwner()},
			Handler:     handlePromote,
		},
		{
			Name:        "demote",
			Description: "降为普通用户:/demote <chat_id> 或回复某人(需 owner)",
			Middleware:  []tele.MiddlewareFunc{auth.RequireOwner()},
			Handler:     handleDemote,
		},
	}
}

func handleMembers(c tele.Context) error {
	ms := auth.Members()
	if len(ms) == 0 {
		return c.Send("(暂无特权用户)")
	}
	var sb strings.Builder
	sb.WriteString("特权用户:\n")
	for _, m := range ms {
		fmt.Fprintf(&sb, "%d — %s\n", m.ID, m.Role)
	}
	return c.Send(sb.String())
}

func handlePromote(c tele.Context) error {
	id, ok := targetID(c)
	if !ok {
		return c.Send("用法:/promote <chat_id>,或回复目标用户的消息")
	}
	if auth.RoleOf(id) == auth.RoleOwner {
		return c.Send("对方已是 owner")
	}
	if err := auth.SetRole(id, auth.RoleAdmin); err != nil {
		return c.Send("操作失败:" + err.Error())
	}
	return c.Send(fmt.Sprintf("✅ %d 已提升为 admin", id))
}

func handleDemote(c tele.Context) error {
	id, ok := targetID(c)
	if !ok {
		return c.Send("用法:/demote <chat_id>,或回复目标用户的消息")
	}
	if auth.RoleOf(id) == auth.RoleOwner {
		return c.Send("不能降级 owner")
	}
	if err := auth.SetRole(id, auth.RoleUser); err != nil {
		return c.Send("操作失败:" + err.Error())
	}
	return c.Send(fmt.Sprintf("✅ %d 已降为 user", id))
}

// targetID resolves the target user from the replied-to message or the
// <chat_id> command argument.
func targetID(c tele.Context) (int64, bool) {
	if r := c.Message().ReplyTo; r != nil && r.Sender != nil {
		return r.Sender.ID, true
	}
	if p := strings.TrimSpace(c.Message().Payload); p != "" {
		if id, err := strconv.ParseInt(p, 10, 64); err == nil {
			return id, true
		}
	}
	return 0, false
}

func init() { plugin.Register(Perm{}) }
