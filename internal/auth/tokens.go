package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
)

func hashToken(raw string) []byte {
	sum := sha256.Sum256([]byte(raw))
	return sum[:]
}

func newOpaqueToken(prefix string) (raw string, hash []byte, err error) {
	buf := make([]byte, 32)
	if _, err = rand.Read(buf); err != nil {
		return "", nil, err
	}
	raw = prefix + base64.RawURLEncoding.EncodeToString(buf)
	return raw, hashToken(raw), nil
}

func newRefreshToken() (raw string, hash []byte, err error) {
	return newOpaqueToken("rt_")
}

func newEmailTokenRaw() (raw string, hash []byte, err error) {
	return newOpaqueToken("et_")
}
