package auth

import (
	"fmt"

	tele "gopkg.in/telebot.v3"
)

// RequireRole is a per-command middleware: it only lets senders with role >= min through and rejects everyone else.
// Usage: Command{..., Middleware: []tele.MiddlewareFunc{auth.RequireRole(auth.RoleAdmin)}}
func RequireRole(min Role) tele.MiddlewareFunc {
	return func(next tele.HandlerFunc) tele.HandlerFunc {
		return func(c tele.Context) error {
			if !Has(c.Sender().ID, min) {
				return c.Send(fmt.Sprintf("⛔ 需要 %s 及以上权限", min))
			}
			return next(c)
		}
	}
}

// RequireAdmin requires admin or above (admin, owner).
func RequireAdmin() tele.MiddlewareFunc { return RequireRole(RoleAdmin) }

// RequireOwner only lets the owner through.
func RequireOwner() tele.MiddlewareFunc { return RequireRole(RoleOwner) }
