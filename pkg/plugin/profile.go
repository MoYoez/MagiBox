package plugin

import (
	"log"

	tele "gopkg.in/telebot.v3"
)

// SyncProfile pushes optional profile fields to Telegram via the Bot API:
// display name (setMyName), description (setMyDescription) and about text
// (setMyShortDescription). These mirror BotFather's /setname,
// /setdescription and /setabouttext, so the bot's profile can be managed
// from config instead of chatting with BotFather.
//
// Empty values are skipped. Errors are logged but not fatal (Telegram
// rate-limits name changes aggressively; a failed sync must not block
// startup). Username, avatar and privacy mode remain BotFather-only.
func SyncProfile(b *tele.Bot, name, description, about string) {
	set := func(method, field, value string) {
		if value == "" {
			return
		}
		if _, err := b.Raw(method, map[string]string{field: value}); err != nil {
			log.Printf("[profile] %s 失败: %v", method, err)
			return
		}
		log.Printf("[profile] %s 已同步", method)
	}
	set("setMyName", "name", name)
	set("setMyDescription", "description", description)
	set("setMyShortDescription", "short_description", about)
}
