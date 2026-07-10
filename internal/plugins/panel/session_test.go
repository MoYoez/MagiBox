package panel

import (
	"testing"
	"time"
)

func TestSessionRoundTrip(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	tok := issueSession("secret", now)
	if !validSession("secret", tok, now.Add(time.Hour)) {
		t.Fatal("fresh token should be valid within TTL")
	}
	if validSession("secret", tok, now.Add(sessionTTL+time.Minute)) {
		t.Fatal("token should be expired past TTL")
	}
	if validSession("other-code", tok, now.Add(time.Hour)) {
		t.Fatal("token must not validate under a different code")
	}
	if validSession("secret", "garbage", now) || validSession("secret", "no.dot", now) {
		t.Fatal("malformed tokens must be rejected")
	}
}

func TestSessionTamperResistance(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	tok := issueSession("secret", now)
	// Flip the last signature character.
	bad := tok[:len(tok)-1] + string(rune(tok[len(tok)-1])^1)
	if validSession("secret", bad, now) {
		t.Fatal("tampered signature must be rejected")
	}
}
