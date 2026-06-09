// Package bind provides the /bind command: bind the owner using the one-time
// pairing code printed to the terminal at startup.
package bind

import (
	"fmt"
	"strings"

	tele "gopkg.in/telebot.v3"

	"github.com/moyoez/magibox/internal/auth"
	"github.com/moyoez/magibox/internal/plugin"
)

type Bind struct{ plugin.Base }

func (Bind) Name() string { return "bind" }

func (Bind) Commands() []plugin.Command {
	return []plugin.Command{{
		Name:        "bind",
		Description: "用终端打印的配对码绑定为 owner:/bind <code>",
		Handler: func(c tele.Context) error {
			code := strings.TrimSpace(c.Message().Payload)
			if code == "" {
				return c.Send("用法:/bind <配对码>(配对码见 bot 启动时的终端输出)")
			}
			if auth.Bind(code, c.Sender().ID) {
				return c.Send(fmt.Sprintf("✅ 绑定成功,你已成为 owner(chat id: %d)", c.Sender().ID))
			}
			return c.Send("❌ 配对码无效或已被使用")
		},
	}}
}

func init() { plugin.Register(Bind{}) }
