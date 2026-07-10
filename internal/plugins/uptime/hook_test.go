package uptime

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	up "github.com/moyoez/magibox/internal/uptime"
)

func TestHandleHookUnknownToken(t *testing.T) {
	if err := up.Init(filepath.Join(t.TempDir(), "uptime.json")); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, hookPrefix+"deadbeef", strings.NewReader("{}"))
	rec := httptest.NewRecorder()
	handleHook(nil, rec, req) // unknown token returns before touching the bot
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404", rec.Code)
	}
}

func TestHandleHookUnsetTargetAnswersOK(t *testing.T) {
	if err := up.Init(filepath.Join(t.TempDir(), "uptime.json")); err != nil {
		t.Fatal(err)
	}
	w, err := up.Create("hooktest")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, hookPrefix+w.Token, strings.NewReader(`{"msg":"hi"}`))
	rec := httptest.NewRecorder()
	handleHook(nil, rec, req) // target unset -> answers 200 and skips the push
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	if rec.Body.String() != "ok" {
		t.Fatalf("body = %q, want ok", rec.Body.String())
	}
}
