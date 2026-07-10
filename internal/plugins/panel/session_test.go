package panel

import (
	"path/filepath"
	"testing"
	"time"
)

// initTestStore resets and initializes the panel store in a temp dir so a
// signing secret exists.
func initTestStore(t *testing.T) {
	t.Helper()
	def = &store{codes: map[string]int64{}}
	if err := Init(filepath.Join(t.TempDir(), "panel.json")); err != nil {
		t.Fatal(err)
	}
}

func TestSessionRoundTrip(t *testing.T) {
	initTestStore(t)
	now := time.Unix(1_700_000_000, 0)
	tok := issueSession(now)
	if !validSession(tok, now.Add(24*time.Hour)) {
		t.Fatal("fresh token should be valid within the one-month TTL")
	}
	if validSession(tok, now.Add(sessionTTL+time.Minute)) {
		t.Fatal("token should be expired past TTL")
	}
	if validSession("garbage", now) || validSession("no.dot", now) {
		t.Fatal("malformed tokens must be rejected")
	}
}

func TestSessionTTLIsOneMonth(t *testing.T) {
	if sessionTTL != 30*24*time.Hour {
		t.Fatalf("sessionTTL = %v, want 30 days", sessionTTL)
	}
}

func TestSessionTamperResistance(t *testing.T) {
	initTestStore(t)
	now := time.Unix(1_700_000_000, 0)
	tok := issueSession(now)
	bad := tok[:len(tok)-1] + string(rune(tok[len(tok)-1])^1)
	if validSession(bad, now) {
		t.Fatal("tampered signature must be rejected")
	}
}

func TestOneTimeCodeConsumed(t *testing.T) {
	initTestStore(t)
	code, err := NewCode(time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}
	if !ConsumeCode(code) {
		t.Fatal("first consume should succeed")
	}
	if ConsumeCode(code) {
		t.Fatal("one-time code must not be usable twice")
	}
}

func TestRevokeCode(t *testing.T) {
	initTestStore(t)
	code, _ := NewCode(time.Now().Unix())
	if got := ListCodes(); len(got) != 1 {
		t.Fatalf("ListCodes = %v, want 1 pending", got)
	}
	if !Revoke(code) {
		t.Fatal("revoke should succeed for a pending code")
	}
	if ConsumeCode(code) {
		t.Fatal("revoked code must not log in")
	}
}
