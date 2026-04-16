package runtime

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testSecret = "test-secret-key-for-handler-tokens"

func mintTestToken(t *testing.T, tenantID, userID, action, auditOp string) string {
	t.Helper()
	claims := &HandlerClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "declarion",
			Subject:   userID,
			Audience:  jwt.ClaimStrings{HandlerTokenAudience},
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(10 * time.Minute)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ID:        "test-jti",
		},
		UserID:     userID,
		TenantID:   tenantID,
		TenantCode: "test-tenant",
		Action:     action,
		AuditOpID:  auditOp,
		Scope:      HandlerTokenScope,
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(testSecret))
	require.NoError(t, err)
	return signed
}

func setupTestServer(t *testing.T, handlers ...HandlerRegistration) *httptest.Server {
	t.Helper()
	registry := make(map[string]HandlerRegistration, len(handlers))
	for _, h := range handlers {
		registry[h.Method] = h
	}
	cfg := &Config{
		JWTSecret: testSecret,
	}
	cfg.withDefaults()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /rpc", func(w http.ResponseWriter, r *http.Request) {
		handleRPC(w, r, registry, cfg)
	})
	return httptest.NewServer(mux)
}

func setupTestServerWithConfig(t *testing.T, cfg *Config, handlers ...HandlerRegistration) *httptest.Server {
	t.Helper()
	registry := make(map[string]HandlerRegistration, len(handlers))
	for _, h := range handlers {
		registry[h.Method] = h
	}
	cfg.withDefaults()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /rpc", func(w http.ResponseWriter, r *http.Request) {
		handleRPC(w, r, registry, cfg)
	})
	return httptest.NewServer(mux)
}

type echoParams struct {
	Name string `json:"name"`
}

type echoResult struct {
	Message string `json:"message"`
}

func TestHandleRPC_success(t *testing.T) {
	srv := setupTestServer(t, Handler("test.echo", func(ctx *Ctx, p echoParams) (echoResult, error) {
		return echoResult{Message: "hello " + p.Name}, nil
	}))
	defer srv.Close()

	token := mintTestToken(t, "tenant-1", "user-1", "test.echo", "audit-1")
	body := `{"jsonrpc":"2.0","id":"req-1","method":"test.echo","params":{"name":"world"}}`

	req, err := http.NewRequest("POST", srv.URL+"/rpc", strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Declarion-Protocol-Version", "1")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var rpcResp Response
	respBody, _ := io.ReadAll(resp.Body)
	require.NoError(t, json.Unmarshal(respBody, &rpcResp))
	assert.Equal(t, "2.0", rpcResp.JSONRPC)
	assert.Equal(t, "req-1", rpcResp.ID)
	assert.Nil(t, rpcResp.Error)

	resultBytes, _ := json.Marshal(rpcResp.Result)
	var result echoResult
	require.NoError(t, json.Unmarshal(resultBytes, &result))
	assert.Equal(t, "hello world", result.Message)
}

func TestHandleRPC_method_not_found(t *testing.T) {
	srv := setupTestServer(t)
	defer srv.Close()

	body := `{"jsonrpc":"2.0","id":"req-1","method":"nonexistent","params":{}}`
	resp, err := http.Post(srv.URL+"/rpc", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	var rpcResp Response
	respBody, _ := io.ReadAll(resp.Body)
	require.NoError(t, json.Unmarshal(respBody, &rpcResp))
	assert.Equal(t, JSONRPCMethodNotFound, rpcResp.Error.Code)
}

func TestHandleRPC_invalid_json(t *testing.T) {
	srv := setupTestServer(t)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/rpc", "application/json", strings.NewReader("{invalid"))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	var rpcResp Response
	respBody, _ := io.ReadAll(resp.Body)
	require.NoError(t, json.Unmarshal(respBody, &rpcResp))
	assert.Equal(t, JSONRPCParseError, rpcResp.Error.Code)
}

func TestHandleRPC_protocol_version_mismatch(t *testing.T) {
	srv := setupTestServer(t)
	defer srv.Close()

	body := `{"jsonrpc":"2.0","id":"req-1","method":"test","params":{}}`
	req, err := http.NewRequest("POST", srv.URL+"/rpc", strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Declarion-Protocol-Version", "99")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	var rpcResp Response
	respBody, _ := io.ReadAll(resp.Body)
	require.NoError(t, json.Unmarshal(respBody, &rpcResp))
	assert.Equal(t, JSONRPCAppError, rpcResp.Error.Code)
	assert.Equal(t, CodeProtocolMismatch, rpcResp.Error.Data.DeclarionCode)
	// After fix: req.ID should be available in error response.
	assert.Equal(t, "req-1", rpcResp.ID)
}

func TestHandleRPC_handler_error(t *testing.T) {
	srv := setupTestServer(t, Handler("test.fail", func(ctx *Ctx, p echoParams) (echoResult, error) {
		return echoResult{}, &AppError{
			Code:          JSONRPCAppError,
			Message:       "ClickUp API 429",
			DeclarionCode: CodeExternalService,
			Retryable:     true,
		}
	}))
	defer srv.Close()

	body := `{"jsonrpc":"2.0","id":"req-1","method":"test.fail","params":{"name":"test"}}`
	resp, err := http.Post(srv.URL+"/rpc", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	var rpcResp Response
	respBody, _ := io.ReadAll(resp.Body)
	require.NoError(t, json.Unmarshal(respBody, &rpcResp))
	assert.Equal(t, JSONRPCAppError, rpcResp.Error.Code)
	assert.Equal(t, "ClickUp API 429", rpcResp.Error.Message)
	assert.Equal(t, CodeExternalService, rpcResp.Error.Data.DeclarionCode)
	assert.True(t, rpcResp.Error.Data.Retryable)
}

func TestHandleRPC_invalid_params(t *testing.T) {
	type strictParams struct {
		Count int `json:"count"`
	}
	srv := setupTestServer(t, Handler("test.strict", func(ctx *Ctx, p strictParams) (echoResult, error) {
		return echoResult{Message: "ok"}, nil
	}))
	defer srv.Close()

	// Send string where int expected - Go's json.Unmarshal rejects this.
	body := `{"jsonrpc":"2.0","id":"req-1","method":"test.strict","params":{"count":"not_a_number"}}`
	resp, err := http.Post(srv.URL+"/rpc", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	var rpcResp Response
	respBody, _ := io.ReadAll(resp.Body)
	require.NoError(t, json.Unmarshal(respBody, &rpcResp))
	assert.Equal(t, JSONRPCInvalidParams, rpcResp.Error.Code)
	assert.Equal(t, CodeValidation, rpcResp.Error.Data.DeclarionCode)
}

func TestHandleRPC_invalid_token(t *testing.T) {
	srv := setupTestServer(t, Handler("test.echo", func(ctx *Ctx, p echoParams) (echoResult, error) {
		return echoResult{Message: "ok"}, nil
	}))
	defer srv.Close()

	body := `{"jsonrpc":"2.0","id":"req-1","method":"test.echo","params":{"name":"test"}}`
	req, err := http.NewRequest("POST", srv.URL+"/rpc", strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer invalid-token-garbage")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	var rpcResp Response
	respBody, _ := io.ReadAll(resp.Body)
	require.NoError(t, json.Unmarshal(respBody, &rpcResp))
	assert.Equal(t, JSONRPCAppError, rpcResp.Error.Code)
	assert.Equal(t, CodePermissionDenied, rpcResp.Error.Data.DeclarionCode)
}

func TestHandleRPC_context_propagation(t *testing.T) {
	var capturedCtx *Ctx
	srv := setupTestServer(t, Handler("test.ctx", func(ctx *Ctx, p echoParams) (echoResult, error) {
		capturedCtx = ctx
		return echoResult{Message: "ok"}, nil
	}))
	defer srv.Close()

	token := mintTestToken(t, "tenant-42", "user-99", "test.ctx", "audit-op-123")
	body := `{"jsonrpc":"2.0","id":"req-1","method":"test.ctx","params":{"name":"test"}}`
	req, err := http.NewRequest("POST", srv.URL+"/rpc", strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Declarion-Protocol-Version", "1")
	req.Header.Set("baggage", "declarion.tenant_id=tenant-42")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()

	require.NotNil(t, capturedCtx)
	assert.Equal(t, "tenant-42", capturedCtx.TenantID)
	assert.Equal(t, "test-tenant", capturedCtx.TenantCode)
	assert.Equal(t, "user-99", capturedCtx.UserID)
	assert.Equal(t, "audit-op-123", capturedCtx.AuditOp)
	assert.Equal(t, "test.ctx", capturedCtx.Action)
	assert.Equal(t, "declarion.tenant_id=tenant-42", capturedCtx.Baggage)
	assert.NotNil(t, capturedCtx.Platform)
	assert.NotNil(t, capturedCtx.Logger)
}

func TestHandleRPC_no_token_allowed(t *testing.T) {
	// Without a token, identity fields are empty but request succeeds.
	var capturedCtx *Ctx
	srv := setupTestServer(t, Handler("test.open", func(ctx *Ctx, p echoParams) (echoResult, error) {
		capturedCtx = ctx
		return echoResult{Message: "ok"}, nil
	}))
	defer srv.Close()

	body := `{"jsonrpc":"2.0","id":"req-1","method":"test.open","params":{"name":"test"}}`
	resp, err := http.Post(srv.URL+"/rpc", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	_ = resp.Body.Close()

	require.NotNil(t, capturedCtx)
	assert.Empty(t, capturedCtx.TenantID)
	assert.Empty(t, capturedCtx.UserID)
}

func TestHandleRPC_require_token_rejects_unauthenticated(t *testing.T) {
	cfg := &Config{
		JWTSecret:    testSecret,
		RequireToken: true,
	}
	srv := setupTestServerWithConfig(t, cfg, Handler("test.echo", func(ctx *Ctx, p echoParams) (echoResult, error) {
		return echoResult{Message: "ok"}, nil
	}))
	defer srv.Close()

	body := `{"jsonrpc":"2.0","id":"req-1","method":"test.echo","params":{"name":"test"}}`
	resp, err := http.Post(srv.URL+"/rpc", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	var rpcResp Response
	respBody, _ := io.ReadAll(resp.Body)
	require.NoError(t, json.Unmarshal(respBody, &rpcResp))
	assert.Equal(t, JSONRPCAppError, rpcResp.Error.Code)
	assert.Equal(t, CodePermissionDenied, rpcResp.Error.Data.DeclarionCode)
}

func TestHandleRPC_require_token_allows_authenticated(t *testing.T) {
	cfg := &Config{
		JWTSecret:    testSecret,
		RequireToken: true,
	}
	srv := setupTestServerWithConfig(t, cfg, Handler("test.echo", func(ctx *Ctx, p echoParams) (echoResult, error) {
		return echoResult{Message: "hello " + p.Name}, nil
	}))
	defer srv.Close()

	token := mintTestToken(t, "t1", "u1", "test.echo", "op1")
	body := `{"jsonrpc":"2.0","id":"req-1","method":"test.echo","params":{"name":"world"}}`
	req, err := http.NewRequest("POST", srv.URL+"/rpc", strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Declarion-Protocol-Version", "1")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	var rpcResp Response
	respBody, _ := io.ReadAll(resp.Body)
	require.NoError(t, json.Unmarshal(respBody, &rpcResp))
	assert.Nil(t, rpcResp.Error)
}

func TestHandleRPC_wrong_jsonrpc_version(t *testing.T) {
	srv := setupTestServer(t)
	defer srv.Close()

	body := `{"jsonrpc":"1.0","id":"req-1","method":"test","params":{}}`
	resp, err := http.Post(srv.URL+"/rpc", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	var rpcResp Response
	respBody, _ := io.ReadAll(resp.Body)
	require.NoError(t, json.Unmarshal(respBody, &rpcResp))
	assert.Equal(t, JSONRPCInvalidRequest, rpcResp.Error.Code)
}
