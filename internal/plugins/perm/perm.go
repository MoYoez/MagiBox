// Package perm provides role and composable permission management commands.
package perm

import (
	"fmt"
	"strconv"
	"strings"

	tele "gopkg.in/telebot.v3"

	"github.com/moyoez/magibox/internal/auth"
	"github.com/moyoez/magibox/pkg/plugin"
)

type Perm struct{ plugin.Base }

func (Perm) Name() string { return "perm" }

func (Perm) Commands() []plugin.Command {
	return []plugin.Command{
		{
			Name:        "members",
			Description: "列出所有角色与业务权限(需 admin)",
			Middleware:  []tele.MiddlewareFunc{auth.RequireAdmin()},
			Handler:     handleMembers,
		},
		{
			Name:        "permission",
			Description: "叠加授权:/permission grant|revoke|show|list(需 owner)",
			Middleware:  []tele.MiddlewareFunc{auth.RequireOwner()},
			Handler:     handlePermission,
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
		return c.Send("(暂无角色或业务权限)")
	}
	var sb strings.Builder
	sb.WriteString("授权成员:\n")
	for _, m := range ms {
		fmt.Fprintf(&sb, "%d — %s", m.ID, m.Role)
		if len(m.Permissions) > 0 {
			fmt.Fprintf(&sb, " — %s", formatPermissions(m.Permissions))
		}
		sb.WriteByte('\n')
	}
	return c.Send(sb.String())
}

const permissionUsage = `权限用法(需 owner):
/permission list
/permission show <chat_id>（也可回复目标消息）
/permission grant <chat_id> <permission...>
/permission revoke <chat_id> <permission...>
回复目标消息时可省略 chat_id。多个 permission 会叠加，不会覆盖现有授权。`

func handlePermission(c tele.Context) error {
	message := c.Message()
	if message == nil {
		return c.Send(permissionUsage)
	}
	fields := strings.Fields(message.Payload)
	if len(fields) == 0 {
		return c.Send(permissionUsage)
	}
	switch strings.ToLower(fields[0]) {
	case "list":
		return handleMembers(c)
	case "show":
		id, _, ok := permissionTarget(c, fields[1:], true)
		if !ok {
			return c.Send(permissionUsage)
		}
		return c.Send(formatAuthorization(id))
	case "grant", "revoke":
		id, permissionFields, ok := permissionTarget(c, fields[1:], false)
		if !ok {
			return c.Send(permissionUsage)
		}
		permissions, err := parsePermissions(permissionFields)
		if err != nil {
			return c.Send("权限格式错误：" + err.Error())
		}
		if strings.EqualFold(fields[0], "grant") {
			if err := auth.GrantPermissions(id, permissions...); err != nil {
				return c.Send("授权失败：" + err.Error())
			}
			return c.Send("✅ 已叠加授权\n" + formatAuthorization(id))
		}
		if err := auth.RevokePermissions(id, permissions...); err != nil {
			return c.Send("撤权失败：" + err.Error())
		}
		return c.Send("✅ 已撤销指定权限\n" + formatAuthorization(id))
	default:
		return c.Send(permissionUsage)
	}
}

func permissionTarget(c tele.Context, fields []string, defaultToSender bool) (int64, []string, bool) {
	if message := c.Message(); message != nil && message.ReplyTo != nil && message.ReplyTo.Sender != nil {
		return message.ReplyTo.Sender.ID, fields, true
	}
	if len(fields) > 0 {
		id, err := strconv.ParseInt(fields[0], 10, 64)
		if err == nil && id != 0 {
			return id, fields[1:], true
		}
	}
	if defaultToSender && c.Sender() != nil {
		return c.Sender().ID, fields, true
	}
	return 0, nil, false
}

func parsePermissions(fields []string) ([]auth.Permission, error) {
	if len(fields) == 0 {
		return nil, fmt.Errorf("至少提供一个 permission")
	}
	permissions := make([]auth.Permission, 0, len(fields))
	for _, field := range fields {
		permission, ok := auth.ParsePermission(field)
		if !ok {
			return nil, fmt.Errorf("%q 不是有效 permission", field)
		}
		permissions = append(permissions, permission)
	}
	return permissions, nil
}

func formatPermissions(permissions []auth.Permission) string {
	values := make([]string, len(permissions))
	for index, permission := range permissions {
		values[index] = permission.String()
	}
	return strings.Join(values, ", ")
}

func formatAuthorization(id int64) string {
	permissions := auth.Permissions(id)
	permissionText := "(无)"
	if len(permissions) > 0 {
		permissionText = formatPermissions(permissions)
	}
	return fmt.Sprintf("%d — role=%s — permissions=%s", id, auth.RoleOf(id), permissionText)
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
