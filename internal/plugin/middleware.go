package plugin

import (
	"fmt"
	"log"
	"time"

	tele "gopkg.in/telebot.v3"
)

// Logger logs each command's sender, text, and duration.
func Logger() tele.MiddlewareFunc {
	return func(next tele.HandlerFunc) tele.HandlerFunc {
		return func(c tele.Context) error {
			start := time.Now()
			err := next(c)
			log.Printf("[msg] from=%s text=%q took=%s err=%v",
				c.Sender().Username, c.Text(), time.Since(start), err)
			return err
		}
	}
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
