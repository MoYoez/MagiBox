// Package bundle provides the /bundle command: collect a stretch of
// conversation (text + photos/stickers/videos) and package it into an
// accessible URL. Documents are not collected; oversized videos are skipped.
package bundle

import (
	"fmt"
	"io"
	"strings"

	tele "gopkg.in/telebot.v3"

	bd "github.com/moyoez/magibox/internal/bundle"
	"github.com/moyoez/magibox/internal/config"
	"github.com/moyoez/magibox/internal/plugin"
)

const (
	maxVideoBytes = 20 << 20 // videos larger than this are not bundled (also the Bot API getFile download limit)
	maxMediaBytes = 50 << 20 // read limit per media download
)

type Plugin struct{ plugin.Base }

func (Plugin) Name() string { return "bundle" }

func (Plugin) Commands() []plugin.Command {
	return []plugin.Command{{
		Name:        "bundle",
		Description: "打包会话成 URL:/bundle start|end|status|cancel",
		Handler:     handle,
	}}
}

// Wire implements plugin.Wirer: while collecting, record text and
// photos/stickers/videos (documents excluded).
func (Plugin) Wire(b *tele.Bot) {
	b.Handle(tele.OnText, func(c tele.Context) error {
		name, username := senderOf(c)
		bd.Add(c.Chat().ID, name, username, c.Text())
		return nil
	})
	b.Handle(tele.OnPhoto, func(c tele.Context) error {
		if p := c.Message().Photo; p != nil {
			saveMedia(b, c, "photo", &p.File, "jpg", c.Message().Caption)
		}
		return nil
	})
	b.Handle(tele.OnSticker, func(c tele.Context) error {
		if s := c.Message().Sticker; s != nil {
			saveMedia(b, c, "sticker", &s.File, "webp", "")
		}
		return nil
	})
	b.Handle(tele.OnVideo, func(c tele.Context) error {
		v := c.Message().Video
		if v == nil {
			return nil
		}
		if v.FileSize > maxVideoBytes {
			name, username := senderOf(c)
			bd.Add(c.Chat().ID, name, username, "[视频过大,未打包]")
			return nil
		}
		saveMedia(b, c, "video", &v.File, "mp4", c.Message().Caption)
		return nil
	})
	// tele.OnDocument is intentionally not registered: documents are not bundled.
}

// saveMedia downloads media and hands it to the bundle store (only while the
// chat is collecting).
func saveMedia(b *tele.Bot, c tele.Context, kind string, f *tele.File, ext, caption string) {
	if on, _ := bd.Status(c.Chat().ID); !on {
		return // not collecting — skip the download to save bandwidth
	}
	rc, err := b.File(f)
	if err != nil {
		return
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, maxMediaBytes))
	if err != nil {
		return
	}
	name, username := senderOf(c)
	bd.AddMedia(c.Chat().ID, name, username, kind, caption, data, ext)
}

func handle(c tele.Context) error {
	args := strings.Fields(c.Message().Payload)
	sub := "status"
	if len(args) > 0 {
		sub = args[0]
	}
	chat := c.Chat().ID
	switch sub {
	case "start":
		title := c.Chat().Title
		if title == "" {
			name, _ := senderOf(c)
			title = name + " 的会话"
		}
		if err := bd.Start(chat, title); err != nil {
			return c.Send("失败:" + err.Error())
		}
		return c.Send("🟢 开始收集消息(文本 + 图片/sticker/视频),聊完发 /bundle end 打包")

	case "end":
		b, err := bd.End(chat)
		if err != nil {
			return c.Send("失败:" + err.Error())
		}
		url := config.BundleBaseURL() + "/b/" + b.ID
		return c.Send(fmt.Sprintf(
			"✅ 已打包 %d 条消息:\n%s\n\n浏览器打开看聊天样式;curl 或加 ?format=json 拿 JSON 喂给 AI。",
			len(b.Messages), url))

	case "status":
		on, n := bd.Status(chat)
		if !on {
			return c.Send("当前没有在收集。/bundle start 开始")
		}
		return c.Send(fmt.Sprintf("🟢 收集中,已记录 %d 条。/bundle end 打包", n))

	case "cancel":
		if bd.Cancel(chat) {
			return c.Send("🗑 已取消收集")
		}
		return c.Send("当前没有在收集")

	default:
		return c.Send("用法:/bundle start|end|status|cancel")
	}
}

// senderOf returns the sender's display name and optional username (without
// @). The display name prefers first/last name, falling back to username,
// then to user{id}.
func senderOf(c tele.Context) (name, username string) {
	u := c.Sender()
	if u == nil {
		return "unknown", ""
	}
	username = u.Username
	if n := strings.TrimSpace(u.FirstName + " " + u.LastName); n != "" {
		return n, username
	}
	if username != "" {
		return username, username
	}
	return fmt.Sprintf("user%d", u.ID), ""
}

func init() { plugin.Register(Plugin{}) }
