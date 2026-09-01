package account

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
)

const encryptionKeySize = 32

type tokenCipher struct {
	aead cipher.AEAD
}

func newTokenCipher(encoded string) (*tokenCipher, error) {
	key, err := decodeEncryptionKey(encoded)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("oidc: create token cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("oidc: create token AEAD: %w", err)
	}
	return &tokenCipher{aead: aead}, nil
}

func decodeEncryptionKey(encoded string) ([]byte, error) {
	encodings := []*base64.Encoding{
		base64.RawURLEncoding,
		base64.URLEncoding,
		base64.RawStdEncoding,
		base64.StdEncoding,
	}
	for _, encoding := range encodings {
		decoded, err := encoding.DecodeString(encoded)
		if err == nil && len(decoded) == encryptionKeySize {
			return decoded, nil
		}
	}
	return nil, fmt.Errorf("oidc: MAGI_OIDC_ENCRYPTION_KEY must be a base64 encoded 32-byte key")
}

func (c *tokenCipher) seal(plaintext, additionalData string) (string, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("oidc: generate token nonce: %w", err)
	}
	sealed := c.aead.Seal(nonce, nonce, []byte(plaintext), []byte(additionalData))
	return "v1." + base64.RawURLEncoding.EncodeToString(sealed), nil
}

func (c *tokenCipher) open(value, additionalData string) (string, error) {
	const prefix = "v1."
	if len(value) <= len(prefix) || value[:len(prefix)] != prefix {
		return "", fmt.Errorf("oidc: unsupported encrypted token format")
	}
	sealed, err := base64.RawURLEncoding.DecodeString(value[len(prefix):])
	if err != nil || len(sealed) <= c.aead.NonceSize() {
		return "", fmt.Errorf("oidc: invalid encrypted token")
	}
	nonce := sealed[:c.aead.NonceSize()]
	plaintext, err := c.aead.Open(nil, nonce, sealed[c.aead.NonceSize():], []byte(additionalData))
	if err != nil {
		return "", fmt.Errorf("oidc: decrypt refresh token: %w", err)
	}
	return string(plaintext), nil
}
