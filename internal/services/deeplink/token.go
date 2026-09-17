package deeplink

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

// GenerateToken produces a high-entropy 128-bit unpadded base64url token and its SHA-256 hash.
func GenerateToken() (token string, hash []byte, err error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", nil, fmt.Errorf("read random bytes: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(buf[:])
	h := sha256.Sum256([]byte(token))
	return token, h[:], nil
}

// HashToken calculates the SHA-256 digest of a raw token string for database lookup.
func HashToken(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}
