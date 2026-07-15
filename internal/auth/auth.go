// Package auth implements OpenStream's token model (SPEC.md §10): JWTs
// signed with the app secret (HS256 today; the Verifier is an interface
// point for RS256/EdDSA later), server tokens with on-behalf-of, guest
// tokens, development tokens, and revocation watermarks.
package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Claims are the OpenStream JWT claims (SPEC.md §10).
type Claims struct {
	UserID string `json:"user_id,omitempty"`
	Server bool   `json:"server,omitempty"`
	Role   string `json:"role,omitempty"` // used by guest tokens
	jwt.RegisteredClaims
}

// Token verification errors mapped to API error codes by the caller.
var (
	ErrTokenInvalid   = errors.New("auth: token invalid")
	ErrTokenExpired   = errors.New("auth: token expired")
	ErrTokenRevoked   = errors.New("auth: token revoked")
	ErrTokenNotServer = errors.New("auth: server token required")
)

// MintUserToken signs a user token with the app secret. exp<=0 means no
// expiry (long-lived tokens are the developer's choice, as with Stream).
func MintUserToken(apiSecret, userID string, exp time.Duration) (string, error) {
	return mint(apiSecret, Claims{
		UserID: userID,
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: expiry(exp),
		},
	})
}

// MintServerToken signs a server-side token ({"server": true}).
func MintServerToken(apiSecret string) (string, error) {
	return mint(apiSecret, Claims{
		Server: true,
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt: jwt.NewNumericDate(time.Now()),
		},
	})
}

// MintGuestToken signs a token carrying the guest role (SPEC.md §5.3 U6).
func MintGuestToken(apiSecret, userID string, exp time.Duration) (string, error) {
	return mint(apiSecret, Claims{
		UserID: userID,
		Role:   "guest",
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: expiry(exp),
		},
	})
}

func expiry(exp time.Duration) *jwt.NumericDate {
	if exp == 0 {
		return nil
	}
	return jwt.NewNumericDate(time.Now().Add(exp))
}

func mint(apiSecret string, claims Claims) (string, error) {
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(apiSecret))
	if err != nil {
		return "", fmt.Errorf("auth: sign: %w", err)
	}
	return signed, nil
}

// Verify parses and validates a token against the app secret.
func Verify(apiSecret, tokenString string) (*Claims, error) {
	claims := &Claims{}
	token, err := jwt.ParseWithClaims(tokenString, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("%w: unexpected signing method %v", ErrTokenInvalid, t.Header["alg"])
		}
		return []byte(apiSecret), nil
	})
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, ErrTokenExpired
		}
		return nil, errors.Join(ErrTokenInvalid, err)
	}
	if !token.Valid {
		return nil, ErrTokenInvalid
	}
	if !claims.Server && claims.UserID == "" {
		return nil, fmt.Errorf("%w: missing user_id", ErrTokenInvalid)
	}
	return claims, nil
}

// VerifyDev parses an unsigned development token (disable_auth_checks apps
// only, SPEC.md §10). Signature is NOT checked.
func VerifyDev(tokenString string) (*Claims, error) {
	claims := &Claims{}
	parser := jwt.NewParser()
	if _, _, err := parser.ParseUnverified(tokenString, claims); err != nil {
		return nil, errors.Join(ErrTokenInvalid, err)
	}
	if !claims.Server && claims.UserID == "" {
		return nil, fmt.Errorf("%w: missing user_id", ErrTokenInvalid)
	}
	return claims, nil
}

// CheckRevocation enforces revoke_tokens_issued_before watermarks at the
// app and user level (SPEC.md §10).
func CheckRevocation(claims *Claims, appWatermark, userWatermark *time.Time) error {
	if claims.IssuedAt == nil {
		return nil // tokens without iat cannot be watermark-revoked
	}
	iat := claims.IssuedAt.Time
	if appWatermark != nil && iat.Before(*appWatermark) {
		return ErrTokenRevoked
	}
	if userWatermark != nil && iat.Before(*userWatermark) {
		return ErrTokenRevoked
	}
	return nil
}
