package runtime

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mintVerifierToken(t *testing.T, method, action string) string {
	t.Helper()
	claims := &VerifierClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "declarion",
			Audience:  jwt.ClaimStrings{VerifierTokenAudience},
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(10 * time.Minute)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ID:        "test-jti",
		},
		Method: method,
		Action: action,
		Scope:  VerifierTokenScope,
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(testSecret))
	require.NoError(t, err)
	return signed
}

// mintTestTokenWithMethod mints a handler token carrying an exact-method claim.
func mintTestTokenWithMethod(t *testing.T, method string) string {
	t.Helper()
	claims := &HandlerClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "declarion",
			Audience:  jwt.ClaimStrings{HandlerTokenAudience},
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(10 * time.Minute)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ID:        "test-jti",
		},
		UserID:   "user-1",
		TenantID: "tenant-1",
		Action:   "some.action",
		Scope:    HandlerTokenScope,
		Method:   method,
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(testSecret))
	require.NoError(t, err)
	return signed
}

func externalEnvelopeBody(id, method string, rawBody string) string {
	env := map[string]any{
		"_external_request": map[string]any{
			"action_code":     "community.webhook.telegram",
			"verifier_code":   method,
			"http_method":     "POST",
			"path":            "/bot-1",
			"path_values":     map[string]string{"bot_id": "bot-1"},
			"query":           map[string][]string{},
			"headers":         map[string][]string{"x-telegram-bot-api-secret-token": {"s3cr3t"}},
			"raw_body_base64": base64.StdEncoding.EncodeToString([]byte(rawBody)),
			"request_id":      id,
			"remote_address":  "203.0.113.10",
		},
	}
	params, _ := json.Marshal(env)
	req := map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": json.RawMessage(params)}
	body, _ := json.Marshal(req)
	return string(body)
}

func postRPC(t *testing.T, url, body string, headers map[string]string) Response {
	t.Helper()
	req, err := http.NewRequest("POST", url+"/rpc", strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Declarion-Protocol-Version", "1")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	var rpcResp Response
	raw, _ := io.ReadAll(resp.Body)
	require.NoError(t, json.Unmarshal(raw, &rpcResp))
	return rpcResp
}

const telegramVerifier = "community.telegram"

func TestVerifierDispatch_Success(t *testing.T) {
	ClearVerifierRegistry()
	t.Cleanup(ClearVerifierRegistry)
	var gotBody string
	var gotPlatformNil bool
	RegisterVerifier(telegramVerifier, func(c *VerifierCtx) (VerifierResult, error) {
		gotBody = string(c.RawBody)
		gotPlatformNil = c.Platform == nil
		return VerifierResult{
			TargetTenantID: "11111111-1111-1111-1111-111111111111",
			UserID:         "22222222-2222-2222-2222-222222222222",
			Params:         map[string]any{"secret_seen": c.Header("X-Telegram-Bot-Api-Secret-Token"), "bot_id": c.PathValues["bot_id"]},
		}, nil
	})
	srv := setupTestServer(t)
	defer srv.Close()

	body := externalEnvelopeBody("req-1", telegramVerifier, `{"update_id":123}`)
	token := mintVerifierToken(t, telegramVerifier, "community.webhook.telegram")
	resp := postRPC(t, srv.URL, body, map[string]string{"Authorization": "Bearer " + token})

	require.Nil(t, resp.Error, "unexpected error: %+v", resp.Error)
	assert.Equal(t, `{"update_id":123}`, gotBody)
	assert.True(t, gotPlatformNil, "Platform must be nil without a run-as credential")
	res, _ := json.Marshal(resp.Result)
	var vr VerifierResult
	require.NoError(t, json.Unmarshal(res, &vr))
	assert.Equal(t, "22222222-2222-2222-2222-222222222222", vr.UserID)
	assert.Equal(t, "s3cr3t", vr.Params["secret_seen"])
}

func TestVerifierDispatch_RunAsBuildsPlatform(t *testing.T) {
	ClearVerifierRegistry()
	t.Cleanup(ClearVerifierRegistry)
	var platformPresent bool
	RegisterVerifier(telegramVerifier, func(c *VerifierCtx) (VerifierResult, error) {
		platformPresent = c.Platform != nil
		return VerifierResult{TargetTenantID: "11111111-1111-1111-1111-111111111111", UserID: "22222222-2222-2222-2222-222222222222"}, nil
	})
	srv := setupTestServer(t)
	defer srv.Close()

	body := externalEnvelopeBody("req-1", telegramVerifier, `{}`)
	token := mintVerifierToken(t, telegramVerifier, "community.webhook.telegram")
	resp := postRPC(t, srv.URL, body, map[string]string{
		"Authorization":  "Bearer " + token,
		RunAsTokenHeader: "run-as-cred-token",
	})
	require.Nil(t, resp.Error)
	assert.True(t, platformPresent, "Platform must be built from the run-as credential header")
}

func TestVerifierDispatch_MethodMismatch(t *testing.T) {
	ClearVerifierRegistry()
	t.Cleanup(ClearVerifierRegistry)
	RegisterVerifier(telegramVerifier, func(c *VerifierCtx) (VerifierResult, error) {
		return VerifierResult{}, nil
	})
	srv, logs := setupTestServerObservingLogs(t)
	defer srv.Close()

	// Token authorizes a DIFFERENT method than the request calls.
	token := mintVerifierToken(t, "other.verifier", "community.webhook.telegram")
	body := externalEnvelopeBody("req-1", telegramVerifier, `{}`)
	resp := postRPC(t, srv.URL, body, map[string]string{"Authorization": "Bearer " + token})
	require.NotNil(t, resp.Error)
	assert.Equal(t, "auth.invalid_token", resp.Error.Message)
	assert.Contains(t, loggedReason(t, logs), "method mismatch")
}

func TestVerifierToken_OnHandlerMethod_Rejected(t *testing.T) {
	ClearHandlerRegistry()
	ClearVerifierRegistry()
	t.Cleanup(ClearVerifierRegistry)
	RegisterHandler[echoParams, echoResult]("test.echo", func(ctx *HandlerCtx, p echoParams) (echoResult, error) {
		return echoResult{Message: "should not run"}, nil
	})
	srv := setupTestServer(t)
	defer srv.Close()

	// A verifier-audience token calling a HANDLER method routes to the verifier
	// registry (disjoint) -> method not found.
	token := mintVerifierToken(t, "test.echo", "some.action")
	body := `{"jsonrpc":"2.0","id":"req-1","method":"test.echo","params":{"_external_request":{"raw_body_base64":""}}}`
	resp := postRPC(t, srv.URL, body, map[string]string{"Authorization": "Bearer " + token})
	require.NotNil(t, resp.Error)
	assert.Equal(t, JSONRPCMethodNotFound, resp.Error.Code)
}

func TestHandlerToken_OnVerifierMethod_Rejected(t *testing.T) {
	ClearHandlerRegistry()
	ClearVerifierRegistry()
	t.Cleanup(ClearVerifierRegistry)
	RegisterVerifier(telegramVerifier, func(c *VerifierCtx) (VerifierResult, error) {
		return VerifierResult{}, nil
	})
	srv := setupTestServer(t)
	defer srv.Close()

	// A handler-audience token calling a VERIFIER method routes to the handler
	// registry (disjoint) -> method not found. Plain params (a handler token
	// never carries the external envelope).
	token := mintTestToken(t, "tenant-1", "user-1", "community.telegram", "audit-1")
	body := `{"jsonrpc":"2.0","id":"req-1","method":"community.telegram","params":{}}`
	resp := postRPC(t, srv.URL, body, map[string]string{"Authorization": "Bearer " + token})
	require.NotNil(t, resp.Error)
	assert.Equal(t, JSONRPCMethodNotFound, resp.Error.Code)
}

func TestVerifierDispatch_UnsignedRefused(t *testing.T) {
	ClearVerifierRegistry()
	t.Cleanup(ClearVerifierRegistry)
	RegisterVerifier(telegramVerifier, func(c *VerifierCtx) (VerifierResult, error) {
		return VerifierResult{}, nil
	})
	// Server with NO secret: verifier methods must be refused.
	cfg := &Config{}
	srv := setupTestServerWithConfig(t, cfg)
	defer srv.Close()

	token := mintVerifierToken(t, telegramVerifier, "community.webhook.telegram")
	body := externalEnvelopeBody("req-1", telegramVerifier, `{}`)
	resp := postRPC(t, srv.URL, body, map[string]string{"Authorization": "Bearer " + token})
	require.NotNil(t, resp.Error)
	assert.Equal(t, "auth.unauthorized", resp.Error.Message)
}

func TestHandlerToken_MethodBinding(t *testing.T) {
	ClearHandlerRegistry()
	RegisterHandler[echoParams, echoResult]("test.echo", func(ctx *HandlerCtx, p echoParams) (echoResult, error) {
		return echoResult{Message: "ran"}, nil
	})
	srv, logs := setupTestServerObservingLogs(t)
	defer srv.Close()

	// Token method claim names a DIFFERENT method than the request -> reject.
	token := mintTestTokenWithMethod(t, "other.method")
	body := `{"jsonrpc":"2.0","id":"req-1","method":"test.echo","params":{"name":"x"}}`
	resp := postRPC(t, srv.URL, body, map[string]string{"Authorization": "Bearer " + token})
	require.NotNil(t, resp.Error)
	assert.Equal(t, "auth.invalid_token", resp.Error.Message)
	assert.Contains(t, loggedReason(t, logs), "method mismatch")
}

func TestServe_VerifierRequiresSignedBoot(t *testing.T) {
	ClearVerifierRegistry()
	t.Cleanup(ClearVerifierRegistry)
	RegisterVerifier(telegramVerifier, func(c *VerifierCtx) (VerifierResult, error) { return VerifierResult{}, nil })
	// Allow the unsigned handler path (test-only) but keep no secret: a
	// deployment that registers a verifier must still refuse to boot.
	t.Setenv("DECLARION_SIDECAR_ALLOW_UNVERIFIED", "1")
	t.Setenv("DECLARION_JWT_SECRET", "")
	err := Serve(Config{Addr: "127.0.0.1:0", JWTSecret: ""})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "verifiers are registered")
}

func TestRegisterVerifier_DuplicatePanics(t *testing.T) {
	ClearVerifierRegistry()
	t.Cleanup(ClearVerifierRegistry)
	RegisterVerifier(telegramVerifier, func(c *VerifierCtx) (VerifierResult, error) { return VerifierResult{}, nil })
	assert.Panics(t, func() {
		RegisterVerifier(telegramVerifier, func(c *VerifierCtx) (VerifierResult, error) { return VerifierResult{}, nil })
	})
}

func TestReservedParamCollisionRejected(t *testing.T) {
	ClearHandlerRegistry()
	RegisterHandler[echoParams, echoResult]("test.echo", func(ctx *HandlerCtx, p echoParams) (echoResult, error) {
		return echoResult{Message: p.Name}, nil
	})
	srv := setupTestServer(t)
	defer srv.Close()

	token := mintTestToken(t, "tenant-1", "user-1", "test.echo", "audit-1")
	// An unknown reserved (_-prefixed) key must fail closed.
	body := `{"jsonrpc":"2.0","id":"req-1","method":"test.echo","params":{"name":"x","_spoofed":"y"}}`
	resp := postRPC(t, srv.URL, body, map[string]string{"Authorization": "Bearer " + token})
	require.NotNil(t, resp.Error)
	assert.Equal(t, "action.invalid_params", resp.Error.Message)
	param, ok := resp.Error.Data.ExtString("param")
	assert.True(t, ok)
	assert.Equal(t, "_spoofed", param)
}

func TestGenerateFunctionsYAML_VerifiersBlock(t *testing.T) {
	ClearHandlerRegistry()
	ClearVerifierRegistry()
	t.Cleanup(ClearVerifierRegistry)
	RegisterHandler[echoParams, echoResult]("test.echo", func(ctx *HandlerCtx, p echoParams) (echoResult, error) {
		return echoResult{}, nil
	})
	RegisterVerifier(telegramVerifier, func(c *VerifierCtx) (VerifierResult, error) { return VerifierResult{}, nil })

	var buf bytes.Buffer
	require.NoError(t, GenerateFunctionsYAML(&buf))
	out := buf.String()
	assert.Contains(t, out, "handlers:")
	assert.Contains(t, out, "verifiers:")
	assert.Contains(t, out, fmt.Sprintf("  %s:", telegramVerifier))
	// The verifier stub carries the jsonrpc wire declaration.
	idx := strings.Index(out, "verifiers:")
	assert.Contains(t, out[idx:], "type: jsonrpc")
}

// TestVerifierDispatch_OutcomeMapping proves each decline class renders its own
// wire code (Core maps them back to 401 / 400 / 503), and that a verifier that
// returns a PLAIN error - a bug, a leaked infra failure - renders as unavailable
// rather than as a rejection: a permanent 401 would make the provider drop a
// delivery over our own fault.
func TestVerifierDispatch_OutcomeMapping(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		wantCode string
	}{
		{"rejected", Reject("secret mismatch"), CodeVerifierRejected},
		{"invalid_request", InvalidRequest("malformed update"), CodeVerifierInvalidRequest},
		{"unavailable", Unavailable("transition in flight"), CodeVerifierUnavailable},
		{"plain_error_is_unavailable", errors.New("nil map dereference"), CodeVerifierUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ClearVerifierRegistry()
			t.Cleanup(ClearVerifierRegistry)
			RegisterVerifier(telegramVerifier, func(*VerifierCtx) (VerifierResult, error) {
				return VerifierResult{}, tc.err
			})
			srv := setupTestServer(t)
			defer srv.Close()

			token := mintVerifierToken(t, telegramVerifier, "community.webhook.telegram")
			resp := postRPC(t, srv.URL, externalEnvelopeBody("req-1", telegramVerifier, `{}`),
				map[string]string{"Authorization": "Bearer " + token})

			require.NotNil(t, resp.Error, "decline must produce an error envelope")
			require.NotNil(t, resp.Error.Data)
			assert.Equal(t, tc.wantCode, resp.Error.Data.Code())
			// The REASON is internal telemetry, one hop from a public webhook
			// response. It stays in the log line and never on this wire.
			assert.Empty(t, resp.Error.Data.Detail)
			assert.NotContains(t, resp.Error.Message, "secret mismatch")
			assert.NotContains(t, resp.Error.Message, "malformed update")
			assert.NotContains(t, resp.Error.Message, "transition in flight")
		})
	}
}
