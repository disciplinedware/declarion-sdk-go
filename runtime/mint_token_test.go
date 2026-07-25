package runtime

import (
	"testing"
	"time"
)

// A token minted by MintHandlerToken must satisfy the same parse/validate path
// declarion-core applies at the sidecar gate: correct signature, audience,
// issuer, scope, and the required identity claims.
func TestMintHandlerToken_RoundTrip(t *testing.T) {
	const secret = "shared-platform-secret"
	const uid = "11111111-1111-1111-1111-111111111111"
	const tid = "22222222-2222-2222-2222-222222222222"

	tok, err := MintHandlerToken(secret, HandlerTokenParams{
		UserID:      uid,
		TenantID:    tid,
		TenantCode:  "acme",
		Permissions: []string{"action:llm_connector.invoke"},
		Action:      "llm_connector.invoke",
		TTL:         2 * time.Minute,
	})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}

	claims, err := parseHandlerToken(tok, secret)
	if err != nil {
		t.Fatalf("parse (same secret): %v", err)
	}
	if claims.UserID != uid || claims.Subject != uid {
		t.Errorf("uid/sub = %q/%q, want %q", claims.UserID, claims.Subject, uid)
	}
	if claims.TenantID != tid {
		t.Errorf("tid = %q, want %q", claims.TenantID, tid)
	}
	if claims.Action != "llm_connector.invoke" || claims.Method != "llm_connector.invoke" {
		t.Errorf("action/method = %q/%q", claims.Action, claims.Method)
	}
	if claims.Scope != HandlerTokenScope {
		t.Errorf("scope = %q, want %q", claims.Scope, HandlerTokenScope)
	}
	if len(claims.Permissions) != 1 || claims.Permissions[0] != "action:llm_connector.invoke" {
		t.Errorf("perms = %v, want least-privilege connector grant only", claims.Permissions)
	}
	if claims.IsSuperadmin || claims.IsTenantOwner {
		t.Errorf("a least-privilege token must not assert authority bits")
	}
}

func TestMintHandlerToken_WrongSecretRejected(t *testing.T) {
	tok, err := MintHandlerToken("secret-a", HandlerTokenParams{
		UserID: "u", TenantID: "t", Action: "a", TTL: time.Minute,
	})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if _, err := parseHandlerToken(tok, "secret-b"); err == nil {
		t.Fatalf("expected parse failure with a different secret")
	}
}

func TestMintHandlerToken_Validation(t *testing.T) {
	cases := []struct {
		name string
		p    HandlerTokenParams
	}{
		{"missing_tenant", HandlerTokenParams{UserID: "u", Action: "a", TTL: time.Minute}},
		{"missing_action", HandlerTokenParams{UserID: "u", TenantID: "t", TTL: time.Minute}},
		{"missing_user_not_anon", HandlerTokenParams{TenantID: "t", Action: "a", TTL: time.Minute}},
		{"nonpositive_ttl", HandlerTokenParams{UserID: "u", TenantID: "t", Action: "a"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := MintHandlerToken("s", c.p); err == nil {
				t.Fatalf("expected validation error for %s", c.name)
			}
		})
	}
}
