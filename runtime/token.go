package runtime

import (
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

const (
	// HandlerTokenAudience distinguishes handler-dispatch tokens from regular access tokens.
	HandlerTokenAudience = "handler_dispatch"

	// HandlerTokenIssuer is the issuer claim on handler-dispatch tokens.
	HandlerTokenIssuer = "declarion"

	// HandlerTokenGrace pads a minted token's expiry past the caller's declared
	// timeout, matching declarion-core's auth.HandlerTokenGrace so the token
	// stays valid for the full handler window plus clock skew.
	HandlerTokenGrace = 30 * time.Second

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

// HandlerTokenParams are the inputs to MintHandlerToken.
//
// A minted token carries a baked authority snapshot (Permissions plus the
// authority bools): declarion-core trusts these claims at dispatch time and does
// NOT re-resolve the subject from the database. So a caller MUST mint
// least-privilege tokens - only the permissions the call needs - and never assert
// authority it should not have.
type HandlerTokenParams struct {
	// UserID is the acting principal (sub + uid). Required unless Anonymous.
	// It need not reference a provisioned user row, but must be a valid UUID
	// wherever the target action persists it.
	UserID string
	// TenantID (tid) is the acting tenant. Required. A plain handler token is
	// tenant-pinned; the X-Declarion-Tenant-ID header does NOT override it.
	TenantID   string
	TenantCode string
	// Permissions (perms) is the baked authority snapshot the target action
	// gates on. Keep it least-privilege.
	Permissions []string
	// Action (action + method) is the action code this token authorizes. Required.
	Action string
	// TTL sets exp = now + TTL + HandlerTokenGrace. Keep it short.
	TTL time.Duration
	// Authority bits, default false. Set only when the acting principal
	// genuinely holds them; never assert superadmin for a scoped call.
	IsSuperadmin  bool
	IsTenantOwner bool
	IsGlobalUser  bool
	// AuditOpID correlates the call in audit; optional.
	AuditOpID string
	// Anonymous marks an unauthenticated continuation (UserID may be empty).
	Anonymous bool
}

// MintHandlerToken signs a handler-dispatch (continuation) token with the shared
// platform JWT secret, byte-compatible with declarion-core's
// auth.HandlerTokenManager.Mint. It exists for the case where a trusted operator
// sidecar must ORIGINATE a callable_from:sidecar call rather than ride an inbound
// Core dispatch (which already hands the sidecar a token to reuse): the sidecar
// mints one for the acting principal and presents it as the Bearer credential.
//
// SECURITY: possession of the secret is the entire trust boundary (HS256 is
// symmetric - the same secret validates and signs). Mint only least-privilege
// tokens, for a verified or explicitly-trusted subject, with a short TTL.
func MintHandlerToken(jwtSecret string, p HandlerTokenParams) (string, error) {
	if jwtSecret == "" {
		return "", fmt.Errorf("mint handler token: empty jwt secret")
	}
	if p.TenantID == "" {
		return "", fmt.Errorf("mint handler token: TenantID required")
	}
	if p.Action == "" {
		return "", fmt.Errorf("mint handler token: Action required")
	}
	if !p.Anonymous && p.UserID == "" {
		return "", fmt.Errorf("mint handler token: UserID required unless Anonymous")
	}
	if p.TTL <= 0 {
		return "", fmt.Errorf("mint handler token: TTL must be positive")
	}
	now := time.Now()
	claims := &HandlerClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    HandlerTokenIssuer,
			Subject:   p.UserID,
			Audience:  jwt.ClaimStrings{HandlerTokenAudience},
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(p.TTL + HandlerTokenGrace)),
			ID:        uuid.NewString(),
		},
		UserID:        p.UserID,
		TenantID:      p.TenantID,
		TenantCode:    p.TenantCode,
		Permissions:   p.Permissions,
		IsSuperadmin:  p.IsSuperadmin,
		IsTenantOwner: p.IsTenantOwner,
		IsGlobalUser:  p.IsGlobalUser,
		Action:        p.Action,
		AuditOpID:     p.AuditOpID,
		Scope:         HandlerTokenScope,
		Method:        p.Action,
		Anonymous:     p.Anonymous,
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(jwtSecret))
	if err != nil {
		return "", fmt.Errorf("mint handler token: %w", err)
	}
	return signed, nil
}
