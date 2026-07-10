// Package auth exposes the stable authorization surface intended for plugins.
// Role storage, owner binding, and initialization remain internal to MagiBox.
package auth

import (
	internal "github.com/moyoez/magibox/internal/auth"
	tele "gopkg.in/telebot.v3"
)

type Role = internal.Role

const (
	RoleUser  = internal.RoleUser
	RoleAdmin = internal.RoleAdmin
	RoleOwner = internal.RoleOwner
)

func RoleOf(chatID int64) Role { return internal.RoleOf(chatID) }

func Has(chatID int64, min Role) bool { return internal.Has(chatID, min) }

func RequireRole(min Role) tele.MiddlewareFunc { return internal.RequireRole(min) }

func RequireAdmin() tele.MiddlewareFunc { return internal.RequireAdmin() }

func RequireOwner() tele.MiddlewareFunc { return internal.RequireOwner() }
