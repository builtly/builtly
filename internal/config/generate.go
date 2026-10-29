package config

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
)

// GenerateKeyMaterial returns an "id:base64" segment with size random bytes.
func GenerateKeyMaterial(id string, size int) (string, error) {
	if size < 32 {
		size = 32
	}
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate key: %w", err)
	}
	return id + ":" + base64.StdEncoding.EncodeToString(buf), nil
}

// GenerateAdminToken returns a URL-safe token of at least 32 characters.
func GenerateAdminToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate admin token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// GenerateSetupSecrets creates JWT, encryption, and admin credentials.
func GenerateSetupSecrets() (jwtKeys, encKeys, adminToken string, err error) {
	jwtKeys, err = GenerateKeyMaterial("v1", 32)
	if err != nil {
		return "", "", "", err
	}
	encKeys, err = GenerateKeyMaterial("1", 32)
	if err != nil {
		return "", "", "", err
	}
	adminToken, err = GenerateAdminToken()
	if err != nil {
		return "", "", "", err
	}
	return jwtKeys, encKeys, adminToken, nil
}
