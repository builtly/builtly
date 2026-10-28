package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	argonTime    = 3
	argonMemory  = 64 * 1024 // KiB → 64 MiB
	argonThreads = 2
	argonKeyLen  = 32
	argonSaltLen = 16
)

// dummyHash is used to equalize login timing for unknown emails.
var dummyHash = mustHash("timing-equalization-dummy-password!!")

func mustHash(pw string) string {
	h, err := HashPassword(pw)
	if err != nil {
		panic(err)
	}
	return h
}

// HashPassword returns a PHC argon2id string.
func HashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// VerifyPassword checks password against a PHC argon2id hash.
// needsRehash is true when parameters differ from current defaults.
func VerifyPassword(password, encoded string) (ok bool, needsRehash bool) {
	parts := strings.Split(encoded, "$")
	// "", "argon2id", "v=19", "m=...,t=...,p=...", salt, hash
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, false
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, false
	}
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false, false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, false
	}
	got := argon2.IDKey([]byte(password), salt, t, m, p, uint32(len(want)))
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return false, false
	}
	needsRehash = m != argonMemory || t != argonTime || p != argonThreads || len(want) != argonKeyLen
	return true, needsRehash
}

// ValidatePasswordLength enforces NIST 800-63B length-only policy.
func ValidatePasswordLength(password string) error {
	n := len(password)
	if n < 8 || n > 128 {
		return ErrWeakPassword
	}
	return nil
}
