package plugin

import (
	"fmt"
	"log"
	"strings"
	"time"

	tele "gopkg.in/telebot.v3"
)

// Logger logs each command's sender, command name, and duration. Message bodies
// and command arguments are never logged because they may contain credentials.
func Logger() tele.MiddlewareFunc {
	return func(next tele.HandlerFunc) tele.HandlerFunc {
		return func(c tele.Context) error {
			start := time.Now()
			err := next(c)
			log.Printf("[msg] from=%s text=%q took=%s err=%v",
				c.Sender().Username, safeLogText(c.Text()), time.Since(start), err)
			return err
		}
	}
}

func safeLogText(text string) string {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return "[empty message]"
	}
	if strings.HasPrefix(fields[0], "/") {
		if len(fields) == 1 {
			return fields[0]
		}
		return fields[0] + " [arguments redacted]"
	}
	return "[message redacted]"
}

// Recover catches panics inside handlers so a single message cannot crash the whole bot.
func Recover() tele.MiddlewareFunc {
	return func(next tele.HandlerFunc) tele.HandlerFunc {
		return func(c tele.Context) (err error) {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[panic] recovered: %v", r)
					err = fmt.Errorf("internal error")
				}
			}()
			return next(c)
		}
	}
}
