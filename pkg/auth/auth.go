// Package auth exposes the stable authorization surface intended for plugins.
// Role storage, owner binding, and initialization remain internal to MagiBox.
package auth

import (
	internal "github.com/moyoez/magibox/internal/auth"
	tele "gopkg.in/telebot.v3"
)

type Role = internal.Role
type Permission = internal.Permission

const (
	RoleUser  = internal.RoleUser
	RoleAdmin = internal.RoleAdmin
	RoleOwner = internal.RoleOwner
)

func RoleOf(chatID int64) Role { return internal.RoleOf(chatID) }

func Has(chatID int64, min Role) bool { return internal.Has(chatID, min) }

func ParsePermission(value string) (Permission, bool) { return internal.ParsePermission(value) }

func HasPermission(chatID int64, permission Permission) bool {
	return internal.HasPermission(chatID, permission)
}

func Permissions(chatID int64) []Permission { return internal.Permissions(chatID) }

func RequireRole(min Role) tele.MiddlewareFunc { return internal.RequireRole(min) }

func RequireAdmin() tele.MiddlewareFunc { return internal.RequireAdmin() }

func RequireOwner() tele.MiddlewareFunc { return internal.RequireOwner() }

func RequirePermission(permission Permission) tele.MiddlewareFunc {
	return internal.RequirePermission(permission)
}
