package runtime

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseHandlerToken_valid(t *testing.T) {
	token := mintTestToken(t, "t1", "u1", "clickup.import", "op1")
	claims, err := parseHandlerToken(token, testSecret)
	require.NoError(t, err)
	assert.Equal(t, "t1", claims.TenantID)
	assert.Equal(t, "u1", claims.UserID)
	assert.Equal(t, "clickup.import", claims.Action)
	assert.Equal(t, "op1", claims.AuditOpID)
	assert.Equal(t, HandlerTokenScope, claims.Scope)
}

func TestParseHandlerToken_wrong_secret(t *testing.T) {
	token := mintTestToken(t, "t1", "u1", "test", "op1")
	_, err := parseHandlerToken(token, "wrong-secret")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "parse handler token")
}

func TestParseHandlerToken_expired(t *testing.T) {
	claims := &HandlerClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "declarion",
			Subject:   "u1",
			Audience:  jwt.ClaimStrings{HandlerTokenAudience},
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(-1 * time.Minute)),
			IssuedAt:  jwt.NewNumericDate(time.Now().Add(-10 * time.Minute)),
			ID:        "jti",
		},
		UserID:    "u1",
		TenantID:  "t1",
		Action:    "test",
		AuditOpID: "op1",
		Scope:     HandlerTokenScope,
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(testSecret))
	require.NoError(t, err)

	_, err = parseHandlerToken(signed, testSecret)
	assert.Error(t, err)
}

func TestParseHandlerToken_wrong_audience(t *testing.T) {
	claims := &HandlerClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "declarion",
			Subject:   "u1",
			Audience:  jwt.ClaimStrings{"wrong_audience"},
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(10 * time.Minute)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ID:        "jti",
		},
		UserID:    "u1",
		TenantID:  "t1",
		Scope:     HandlerTokenScope,
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(testSecret))
	require.NoError(t, err)

	_, err = parseHandlerToken(signed, testSecret)
	assert.Error(t, err)
}

func TestParseHandlerToken_wrong_scope(t *testing.T) {
	claims := &HandlerClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "declarion",
			Subject:   "u1",
			Audience:  jwt.ClaimStrings{HandlerTokenAudience},
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(10 * time.Minute)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ID:        "jti",
		},
		UserID:    "u1",
		TenantID:  "t1",
		Scope:     "api", // wrong scope
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(testSecret))
	require.NoError(t, err)

	_, err = parseHandlerToken(signed, testSecret)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid handler token scope")
}

func TestParseHandlerToken_no_secret(t *testing.T) {
	// Without secret, parses claims without signature verification.
	token := mintTestToken(t, "t1", "u1", "test", "op1")
	claims, err := parseHandlerToken(token, "")
	require.NoError(t, err)
	assert.Equal(t, "t1", claims.TenantID)
}
