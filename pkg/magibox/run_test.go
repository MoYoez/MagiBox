package magibox

import (
	"strings"
	"testing"
)

func TestRunRequiresBotToken(t *testing.T) {
	t.Setenv("BOT_TOKEN", "")
	err := Run()
	if err == nil {
		t.Fatal("Run() error = nil, want missing BOT_TOKEN error")
	}
	if !strings.Contains(err.Error(), "BOT_TOKEN") {
		t.Fatalf("Run() error = %q, want BOT_TOKEN context", err)
	}
}
