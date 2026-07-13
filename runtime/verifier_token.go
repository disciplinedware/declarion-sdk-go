package runtime

import (
	"fmt"

	"github.com/golang-jwt/jwt/v5"
)

// VerifierClaims is the powerless verifier-dispatch token. It carries NO
// tenant, user, permission, or authority claim - only the exact verifier
// method and the action code that triggered verification, plus registered
// claims (issuer, audience, expiry, jti, iat). Mirrors declarion-core's
// auth.VerifierClaims minted for the verifier call.
type VerifierClaims struct {
	jwt.RegisteredClaims
	// Method is the exact JSON-RPC verifier method this token authorizes. The
	// serve path enforces claim-method == request-method before registry lookup.
	Method string `json:"method"`
	// Action is the external action code whose admission triggered this call.
	Action string `json:"action"`
	// Scope MUST equal VerifierTokenScope.
	Scope string `json:"scope"`
}

// parseVerifierToken validates a verifier-dispatch token. Unlike
// parseHandlerToken there is NO unverified branch: a verifier token is always
// signature-verified (the serve path additionally refuses to serve verifier
// methods when no secret is configured). HS256, issuer, audience, and expiry
// are pinned exactly like the handler token.
func parseVerifierToken(tokenString, jwtSecret string) (*VerifierClaims, error) {
	if jwtSecret == "" {
		return nil, fmt.Errorf("verifier token requires signature verification (no secret configured)")
	}
	opts := []jwt.ParserOption{
		jwt.WithAudience(VerifierTokenAudience),
		jwt.WithIssuer("declarion"),
		jwt.WithExpirationRequired(),
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
	}
	token, err := jwt.ParseWithClaims(tokenString, &VerifierClaims{}, func(t *jwt.Token) (any, error) {
		if t.Method.Alg() != jwt.SigningMethodHS256.Alg() {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return []byte(jwtSecret), nil
	}, opts...)
	if err != nil {
		return nil, fmt.Errorf("parse verifier token: %w", err)
	}
	claims, ok := token.Claims.(*VerifierClaims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("invalid verifier token")
	}
	if claims.Scope != VerifierTokenScope {
		return nil, fmt.Errorf("invalid verifier token scope: %s", claims.Scope)
	}
	if claims.Method == "" || claims.Action == "" {
		return nil, fmt.Errorf("verifier token missing required claim (method/action)")
	}
	return claims, nil
}

// tokenAudience peeks the first audience of a token WITHOUT verifying its
// signature, so the serve path can select the claims family (handler vs
// verifier) and its registry before method lookup. The subsequent
// family-specific parse re-validates the signature and every claim; this peek
// is only a router and grants nothing.
func tokenAudience(tokenString string) (string, error) {
	parser := jwt.NewParser()
	var claims jwt.RegisteredClaims
	if _, _, err := parser.ParseUnverified(tokenString, &claims); err != nil {
		return "", fmt.Errorf("peek token audience: %w", err)
	}
	if len(claims.Audience) == 0 {
		return "", nil
	}
	return claims.Audience[0], nil
}
