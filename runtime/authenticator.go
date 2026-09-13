package runtime

import (
	"context"
	"fmt"
	"net/http"
)

// RequestAuthenticator verifies one JSON-RPC request after its method has been
// decoded. Implementations may return a platform bearer distinct from the
// credential presented to the sidecar.
type RequestAuthenticator interface {
	Authenticate(*http.Request, string) (*AuthenticatedRequest, error)
}

// AuthenticatedRequest is the verified authority and callback credential for
// one JSON-RPC request. Context can carry verifier-specific facts for the
// registered handler without placing them on its typed parameter surface.
type AuthenticatedRequest struct {
	Context       context.Context
	Claims        HandlerClaims
	PlatformToken string
}

func (c *Config) authenticateRequest(r *http.Request, method string) (*AuthenticatedRequest, error) {
	if c.Authenticator != nil {
		verified, err := c.Authenticator.Authenticate(r, method)
		if err != nil {
			return nil, err
		}
		if verified == nil {
			return nil, fmt.Errorf("request authenticator returned no verified request")
		}
		if verified.Context == nil {
			verified.Context = r.Context()
		}
		return verified, nil
	}

	token := extractBearer(r.Header.Get("Authorization"))
	if c.RequireToken && token == "" {
		return nil, fmt.Errorf("authorization bearer is required")
	}
	if token == "" {
		return &AuthenticatedRequest{Context: r.Context()}, nil
	}
	claims, err := parseHandlerToken(token, c.JWTSecret)
	if err != nil {
		return nil, err
	}
	if claims.Method != "" && claims.Method != method {
		return nil, fmt.Errorf("handler token method mismatch: claim %q, request %q", claims.Method, method)
	}
	return &AuthenticatedRequest{Context: r.Context(), Claims: *claims, PlatformToken: token}, nil
}
