package auth

import (
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/builtly/builtly/internal/config"
)

// TokenService issues and verifies access JWTs.
type TokenService struct {
	keys      []config.KeyMaterial
	accessTTL time.Duration
	byKID     map[string][]byte
}

// NewTokenService builds a TokenService from config keys.
func NewTokenService(keys []config.KeyMaterial, accessTTL time.Duration) *TokenService {
	if accessTTL <= 0 {
		accessTTL = 15 * time.Minute
	}
	by := make(map[string][]byte, len(keys))
	for _, k := range keys {
		by[k.ID] = k.Key
	}
	return &TokenService{keys: keys, accessTTL: accessTTL, byKID: by}
}

// AccessTTL returns the configured access token lifetime.
func (t *TokenService) AccessTTL() time.Duration { return t.accessTTL }

type accessClaims struct {
	AMR []string `json:"amr"`
	EV  bool     `json:"ev"`
	jwt.RegisteredClaims
}

// IssueAccess creates a signed access token for a user in an app.
func (t *TokenService) IssueAccess(appID, userID uuid.UUID, amr string, emailVerified bool) (string, time.Duration, error) {
	if len(t.keys) == 0 {
		return "", 0, fmt.Errorf("auth: no signing keys")
	}
	active := t.keys[0]
	now := time.Now().UTC()
	claims := accessClaims{
		AMR: []string{amr},
		EV:  emailVerified,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "builtly",
			Subject:   userID.String(),
			Audience:  []string{appID.String()},
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(t.accessTTL)),
			ID:        uuid.Must(uuid.NewV7()).String(),
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tok.Header["kid"] = active.ID
	signed, err := tok.SignedString(active.Key)
	return signed, t.accessTTL, err
}

// VerifyAccess validates an access token for the expected app audience.
func (t *TokenService) VerifyAccess(tokenString string, appID uuid.UUID) (AccessInfo, error) {
	parsed, err := jwt.ParseWithClaims(tokenString, &accessClaims{}, func(tok *jwt.Token) (any, error) {
		if tok.Method != jwt.SigningMethodHS256 {
			return nil, fmt.Errorf("unexpected alg")
		}
		kid, _ := tok.Header["kid"].(string)
		key, ok := t.byKID[kid]
		if !ok {
			return nil, fmt.Errorf("unknown kid")
		}
		return key, nil
	}, jwt.WithIssuer("builtly"), jwt.WithAudience(appID.String()), jwt.WithLeeway(30*time.Second), jwt.WithValidMethods([]string{"HS256"}))
	if err != nil {
		return AccessInfo{}, err
	}
	claims, ok := parsed.Claims.(*accessClaims)
	if !ok || !parsed.Valid {
		return AccessInfo{}, fmt.Errorf("invalid token")
	}
	uid, err := uuid.Parse(claims.Subject)
	if err != nil {
		return AccessInfo{}, err
	}
	var iat time.Time
	if claims.IssuedAt != nil {
		iat = claims.IssuedAt.Time
	}
	return AccessInfo{
		UserID:        uid,
		IssuedAt:      iat,
		EmailVerified: claims.EV,
		AMR:           claims.AMR,
	}, nil
}
