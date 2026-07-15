package auth

import (
	"errors"
	"testing"
	"time"
)

const secret = "test-secret-please-rotate"

func TestUserTokenRoundTrip(t *testing.T) {
	token, err := MintUserToken(secret, "dhiraj", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := Verify(secret, token)
	if err != nil {
		t.Fatal(err)
	}
	if claims.UserID != "dhiraj" || claims.Server {
		t.Fatalf("claims = %+v", claims)
	}
}

func TestNoExpiryToken(t *testing.T) {
	token, err := MintUserToken(secret, "dhiraj", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(secret, token); err != nil {
		t.Fatalf("no-expiry token must verify: %v", err)
	}
}

func TestExpiredToken(t *testing.T) {
	token, err := MintUserToken(secret, "dhiraj", -time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(secret, token); !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("expected ErrTokenExpired, got %v", err)
	}
}

func TestWrongSecret(t *testing.T) {
	token, err := MintUserToken(secret, "dhiraj", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Verify("other-secret", token); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("expected ErrTokenInvalid, got %v", err)
	}
}

func TestServerToken(t *testing.T) {
	token, err := MintServerToken(secret)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := Verify(secret, token)
	if err != nil {
		t.Fatal(err)
	}
	if !claims.Server {
		t.Fatal("expected server claim")
	}
}

func TestGuestToken(t *testing.T) {
	token, err := MintGuestToken(secret, "guest-1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := Verify(secret, token)
	if err != nil {
		t.Fatal(err)
	}
	if claims.Role != "guest" || claims.UserID != "guest-1" {
		t.Fatalf("claims = %+v", claims)
	}
}

func TestTokenWithoutUserRejected(t *testing.T) {
	token, err := mint(secret, Claims{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(secret, token); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("expected ErrTokenInvalid, got %v", err)
	}
}

func TestRevocationWatermarks(t *testing.T) {
	token, err := MintUserToken(secret, "dhiraj", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := Verify(secret, token)
	if err != nil {
		t.Fatal(err)
	}

	past := time.Now().Add(-time.Hour)
	future := time.Now().Add(time.Hour)

	if err := CheckRevocation(claims, &past, nil); err != nil {
		t.Fatalf("watermark in the past must not revoke: %v", err)
	}
	if err := CheckRevocation(claims, &future, nil); !errors.Is(err, ErrTokenRevoked) {
		t.Fatalf("app watermark after iat must revoke, got %v", err)
	}
	if err := CheckRevocation(claims, nil, &future); !errors.Is(err, ErrTokenRevoked) {
		t.Fatalf("user watermark after iat must revoke, got %v", err)
	}
}

func TestVerifyDevAcceptsUnsigned(t *testing.T) {
	// A token minted with any secret parses in dev mode.
	token, err := MintUserToken("whatever", "dev-user", 0)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := VerifyDev(token)
	if err != nil {
		t.Fatal(err)
	}
	if claims.UserID != "dev-user" {
		t.Fatalf("claims = %+v", claims)
	}
}

func TestVerifyRejectsAlgNone(t *testing.T) {
	// Header {"alg":"none"} with our claims must never verify.
	const algNone = "eyJhbGciOiJub25lIiwidHlwIjoiSldUIn0.eyJ1c2VyX2lkIjoiZXZpbCJ9."
	if _, err := Verify(secret, algNone); err == nil {
		t.Fatal("alg=none token must be rejected")
	}
}
