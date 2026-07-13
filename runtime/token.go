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

	// VerifierTokenAudience distinguishes powerless verifier-dispatch tokens
	// from handler-dispatch tokens. A verifier token carries no tenant, user,
	// permission, or authority claim and is accepted ONLY for the verifier
	// registry.
	VerifierTokenAudience = "verifier_dispatch"

	// VerifierTokenScope is the scope value for verifier-dispatch tokens.
	VerifierTokenScope = "verifier"
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
	// Method is the exact JSON-RPC method (handler code) this token authorizes.
	// When present, the serve path enforces claim-method == request-method so a
	// token minted for one method cannot be replayed on another. Minted by
	// Core; tolerated empty during rollout (older Core mints omit it).
	Method string `json:"method,omitempty"`
	// Anonymous marks tokens minted for unauthenticated handlers (webhook
	// ingress). Such tokens carry no UserID by construction; the platform's
	// RequestVerifier established TenantID before mint. Sidecar handlers
	// running under Anonymous=true see empty UserID and Permissions=[];
	// downstream gates fail closed unless a handler explicitly opts into
	// system-anonymous semantics.
	Anonymous bool `json:"anon,omitempty"`
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
	// Identity-claim guards mirror declarion-core's HandlerTokenManager
	// (handler_token.go::Validate). A token whose payload is missing
	// UserID / TenantID / Action is malformed regardless of signature;
	// without these checks a sidecar handler would receive an anonymous
	// principal and could mistake it for a legitimately empty caller.
	//
	// Anonymous=true tokens (webhook ingress) carry no UserID by design —
	// the verifier established the principal via tenant-only authority.
	// TenantID and Action stay mandatory.
	if !claims.Anonymous && claims.UserID == "" {
		return nil, fmt.Errorf("handler token missing required identity claim (uid)")
	}
	if claims.TenantID == "" || claims.Action == "" {
		return nil, fmt.Errorf("handler token missing required identity claim (tid/action)")
	}

	return claims, nil
}
