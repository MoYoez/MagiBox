package plugin

import (
	"strings"
	"testing"
)

func TestSafeLogTextRedactsAllCommandArguments(t *testing.T) {
	input := "/review ai add kimi https://api.example model sk-super-secret"
	got := safeLogText(input)
	if got != "/review [arguments redacted]" {
		t.Fatalf("safeLogText() = %q", got)
	}
	if strings.Contains(got, "sk-super-secret") || strings.Contains(got, "https://api.example") {
		t.Fatalf("safeLogText() leaked command arguments: %q", got)
	}
}

func TestSafeLogTextKeepsArgumentFreeCommandName(t *testing.T) {
	if got := safeLogText("/ping"); got != "/ping" {
		t.Fatalf("safeLogText(/ping) = %q", got)
	}
}

func TestSafeLogTextRedactsNonCommandMessages(t *testing.T) {
	if got := safeLogText("my password is secret"); got != "[message redacted]" {
		t.Fatalf("safeLogText(non-command) = %q", got)
	}
}
