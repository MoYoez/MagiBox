// Package echo demonstrates a minimal command plugin.
package echo

import (
	tele "gopkg.in/telebot.v3"

	"github.com/moyoez/magibox/internal/plugin"
)

type Echo struct{ plugin.Base }

func (Echo) Name() string { return "echo" }

func (Echo) Commands() []plugin.Command {
	return []plugin.Command{{
		Name:        "echo",
		Description: "复读你发的内容",
		Handler: func(c tele.Context) error {
			if c.Message().Payload == "" {
				return c.Send("用法:/echo <文本>")
			}
			return c.Send(c.Message().Payload)
		},
	}}
}

func init() { plugin.Register(Echo{}) }
