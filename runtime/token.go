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

// HandlerClaims mirrors declarion-core's auth.HandlerClaims (see
// declarion-core/golang/internal/auth/handler_token.go).
// Exported so the conformance harness and tests can use the same type.
type HandlerClaims struct {
	jwt.RegisteredClaims
	UserID     string `json:"uid"`
	TenantID   string `json:"tid"`
	TenantCode string `json:"tcode"`
	// Permissions is the caller's resolved permission list as of token
	// mint. Sidecar handlers gate fine-grained operations with these.
	Permissions []string `json:"perms"`
	// Authority dimensions baked into the token at mint. Sidecar
	// handlers running inside the SDK enforce authority gates via
	// these booleans (cross-tenant decisions, owner-reserved actions,
	// etc.) without re-querying the DB.
	IsSuperadmin  bool   `json:"is_superadmin,omitempty"`
	IsTenantOwner bool   `json:"is_tenant_owner,omitempty"`
	IsGlobalUser  bool   `json:"is_global_user,omitempty"`
	Action        string `json:"action"`
	AuditOpID     string `json:"audit_op"`
	Scope         string `json:"scope"`
}

// parseHandlerToken validates and extracts claims from a continuation token.
// If jwtSecret is empty, the token is decoded without signature verification
// (useful for testing or when the sidecar trusts the network boundary).
func parseHandlerToken(tokenString string, jwtSecret string) (*HandlerClaims, error) {
	// Pin HS256 only — match the platform mint side
	// (declarion-core/internal/auth/handler_token.go::Mint). Accepting
	// sibling HMAC methods (HS384/HS512) widens the signing-method
	// surface unnecessarily and risks alg-confusion attacks.
	opts := []jwt.ParserOption{
		jwt.WithAudience(HandlerTokenAudience),
		jwt.WithIssuer("declarion"),
		jwt.WithExpirationRequired(),
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
	}

	var token *jwt.Token
	var err error

	if jwtSecret != "" {
		token, err = jwt.ParseWithClaims(tokenString, &HandlerClaims{}, func(t *jwt.Token) (any, error) {
			if t.Method.Alg() != jwt.SigningMethodHS256.Alg() {
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
