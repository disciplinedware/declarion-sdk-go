package runtime

import (
	"fmt"

	"github.com/golang-jwt/jwt/v5"
)

const (
	// HandlerTokenAudience distinguishes handler-dispatch tokens from regular access tokens.
	HandlerTokenAudience = "handler_dispatch"

	// HandlerTokenScope is the scope value for handler-dispatch tokens.
	HandlerTokenScope = "handler"
)

// HandlerClaims mirrors declarion-core's auth.HandlerClaims.
// Exported so the conformance harness and tests can use the same type.
type HandlerClaims struct {
	jwt.RegisteredClaims
	UserID     string `json:"uid"`
	TenantID   string `json:"tid"`
	TenantCode string `json:"tcode"`
	Action     string `json:"action"`
	AuditOpID  string `json:"audit_op"`
	Scope      string `json:"scope"`
}

// parseHandlerToken validates and extracts claims from a continuation token.
// If jwtSecret is empty, the token is decoded without signature verification
// (useful for testing or when the sidecar trusts the network boundary).
func parseHandlerToken(tokenString string, jwtSecret string) (*HandlerClaims, error) {
	opts := []jwt.ParserOption{jwt.WithAudience(HandlerTokenAudience), jwt.WithIssuer("declarion"), jwt.WithExpirationRequired()}

	var token *jwt.Token
	var err error

	if jwtSecret != "" {
		token, err = jwt.ParseWithClaims(tokenString, &HandlerClaims{}, func(t *jwt.Token) (any, error) {
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
			}
			return []byte(jwtSecret), nil
		}, opts...)
	} else {
		// No secret: parse without verification (claims-only mode).
		parser := jwt.NewParser(opts...)
		token, _, err = parser.ParseUnverified(tokenString, &HandlerClaims{})
	}

	if err != nil {
		return nil, fmt.Errorf("parse handler token: %w", err)
	}

	claims, ok := token.Claims.(*HandlerClaims)
	if !ok {
		return nil, fmt.Errorf("invalid handler token claims type")
	}

	if jwtSecret != "" && !token.Valid {
		return nil, fmt.Errorf("handler token is not valid")
	}

	if claims.Scope != HandlerTokenScope {
		return nil, fmt.Errorf("invalid handler token scope: %s", claims.Scope)
	}

	return claims, nil
}
