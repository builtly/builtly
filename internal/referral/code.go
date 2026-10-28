package referral

import (
	"crypto/rand"
	"strings"
)

const alphabet = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ"

// GenerateCode returns an 8-character referral code (no ambiguous glyphs).
func GenerateCode() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	out := make([]byte, 8)
	for i := range b {
		out[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(out), nil
}

// NormalizeCode uppercases and strips separators.
func NormalizeCode(code string) string {
	code = strings.ToUpper(strings.TrimSpace(code))
	var b strings.Builder
	for _, r := range code {
		if r == '-' || r == ' ' || r == '_' {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
