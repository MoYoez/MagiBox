package account

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestTokenCipherRoundTripAndAAD(t *testing.T) {
	key := base64.RawURLEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	tokenCipher, err := newTokenCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := tokenCipher.seal("refresh-secret", "binding-a")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sealed, "refresh-secret") {
		t.Fatal("encrypted token contains plaintext")
	}
	opened, err := tokenCipher.open(sealed, "binding-a")
	if err != nil || opened != "refresh-secret" {
		t.Fatalf("open = %q, %v", opened, err)
	}
	if _, err := tokenCipher.open(sealed, "binding-b"); err == nil {
		t.Fatal("different binding AAD must fail")
	}
}

func TestTokenCipherRejectsInvalidKey(t *testing.T) {
	for _, value := range []string{"", "not-base64", base64.StdEncoding.EncodeToString([]byte("too short"))} {
		if _, err := newTokenCipher(value); err == nil {
			t.Fatalf("newTokenCipher(%q) should fail", value)
		}
	}
}
