// Package secrets provides AES-GCM sealing and random token helpers.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/builtly/builtly/internal/config"
)

// Box seals and opens payloads with versioned AES-GCM keys.
type Box struct {
	active config.KeyMaterial
	byID   map[string]config.KeyMaterial
}

// NewBox builds a Box from encryption key material (first key is active).
func NewBox(keys []config.KeyMaterial) (*Box, error) {
	if len(keys) == 0 {
		return nil, fmt.Errorf("secrets: at least one encryption key required")
	}
	byID := make(map[string]config.KeyMaterial, len(keys))
	for _, k := range keys {
		if len(k.Key) != 32 {
			return nil, fmt.Errorf("secrets: key %q must be exactly 32 bytes", k.ID)
		}
		byID[k.ID] = k
	}
	return &Box{active: keys[0], byID: byID}, nil
}

// ActiveVersion returns the numeric active key version for schema.key_version.
func (b *Box) ActiveVersion() int {
	v, err := strconv.Atoi(b.active.ID)
	if err != nil {
		return 0
	}
	return v
}

// Seal encrypts plaintext with AAD. Returns nonce||ciphertext and the active key version.
func (b *Box) Seal(plain, aad []byte) (blob []byte, keyVersion int, err error) {
	block, err := aes.NewCipher(b.active.Key)
	if err != nil {
		return nil, 0, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, 0, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, 0, err
	}
	ct := gcm.Seal(nil, nonce, plain, aad)
	out := make([]byte, 0, len(nonce)+len(ct))
	out = append(out, nonce...)
	out = append(out, ct...)
	return out, b.ActiveVersion(), nil
}

// SealEmbedded encrypts plaintext and prefixes a u32 BE key version (for tables without key_version).
func (b *Box) SealEmbedded(plain, aad []byte) ([]byte, error) {
	blob, ver, err := b.Seal(plain, aad)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 4+len(blob))
	binary.BigEndian.PutUint32(out[:4], uint32(ver))
	copy(out[4:], blob)
	return out, nil
}

// OpenEmbedded decrypts a SealEmbedded blob.
func (b *Box) OpenEmbedded(blob, aad []byte) ([]byte, error) {
	if len(blob) < 4 {
		return nil, fmt.Errorf("secrets: embedded ciphertext too short")
	}
	ver := int(binary.BigEndian.Uint32(blob[:4]))
	return b.Open(blob[4:], ver, aad)
}

// Open decrypts a sealed blob (nonce||ciphertext) with the given key version and AAD.
func (b *Box) Open(blob []byte, keyVersion int, aad []byte) ([]byte, error) {
	id := strconv.Itoa(keyVersion)
	km, ok := b.byID[id]
	if !ok {
		return nil, fmt.Errorf("secrets: unknown key version %d", keyVersion)
	}
	block, err := aes.NewCipher(km.Key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonceSize := gcm.NonceSize()
	if len(blob) < nonceSize+gcm.Overhead() {
		return nil, fmt.Errorf("secrets: ciphertext too short")
	}
	nonce, ct := blob[:nonceSize], blob[nonceSize:]
	return gcm.Open(nil, nonce, ct, aad)
}

// ConnectionAAD builds the associated data for a connection row: id || provider.
func ConnectionAAD(connectionID, provider string) []byte {
	return []byte(connectionID + provider)
}

// RandomBytes returns n cryptographically random bytes.
func RandomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	_, err := io.ReadFull(rand.Reader, b)
	return b, err
}

// HashSHA256 returns the SHA-256 digest of b.
func HashSHA256(b []byte) []byte {
	sum := sha256.Sum256(b)
	return sum[:]
}

// MaskSecret returns a display hint keeping a short prefix and the last 4 characters.
// Example: sk_live_…4f2a
func MaskSecret(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	const keep = 4
	if len(s) <= keep {
		return strings.Repeat("•", len(s))
	}
	prefix := ""
	for _, p := range []string{"sk_live_", "sk_test_", "rk_live_", "rk_test_", "whsec_", "pk_live_", "pk_test_", "bk_"} {
		if strings.HasPrefix(s, p) {
			prefix = p
			break
		}
	}
	if prefix == "" && len(s) > 8 {
		// keep up to first underscore segment or 6 chars
		if i := strings.IndexByte(s, '_'); i > 0 && i < 12 {
			prefix = s[:i+1]
		} else {
			prefix = s[:min(6, len(s)-keep)]
		}
	}
	return prefix + "…" + s[len(s)-keep:]
}

// MaskSecretList masks each string in ss.
func MaskSecretList(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = MaskSecret(s)
	}
	return out
}

// NewAPIKey generates a bk_ API key and its SHA-256 hash.
func NewAPIKey() (raw string, hash []byte, prefix string, err error) {
	b, err := RandomBytes(32)
	if err != nil {
		return "", nil, "", err
	}
	raw = "bk_" + base64.RawURLEncoding.EncodeToString(b)
	hash = HashSHA256([]byte(raw))
	prefix = raw
	if len(prefix) > 12 {
		prefix = prefix[:12]
	}
	return raw, hash, prefix, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
