package runtime

import (
	"github.com/disciplinedware/declarion-sdk-go/errs"

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

// setupTestServer prepares an in-process JSON-RPC server backed by the
// package-level handler registry. Tests register their handlers via
// RegisterHandler BEFORE calling this. ClearHandlerRegistry runs on
// cleanup so neighbouring tests stay isolated.
func setupTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	cfg := &Config{JWTSecret: testSecret}
	cfg.withDefaults()
	return startInProcessServer(t, cfg)
}

func setupTestServerWithConfig(t *testing.T, cfg *Config) *httptest.Server {
	t.Helper()
	cfg.withDefaults()
	return startInProcessServer(t, cfg)
}

func startInProcessServer(t *testing.T, cfg *Config) *httptest.Server {
	t.Helper()
	t.Cleanup(ClearHandlerRegistry)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /rpc", func(w http.ResponseWriter, r *http.Request) {
		handleRPC(w, r, cfg)
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
	ClearHandlerRegistry()
	RegisterHandler[echoParams, echoResult]("test.echo", func(ctx *HandlerCtx, p echoParams) (echoResult, error) {
		return echoResult{Message: "hello " + p.Name}, nil
	})
	srv := setupTestServer(t)
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
	ClearHandlerRegistry()
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
	ClearHandlerRegistry()
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
	ClearHandlerRegistry()
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
	assert.Equal(t, "transport.protocol_mismatch", rpcResp.Error.Data.Code())
	assert.Equal(t, "req-1", rpcResp.ID)
}

func TestHandleRPC_handler_error(t *testing.T) {
	ClearHandlerRegistry()
	RegisterHandler[echoParams, echoResult]("test.fail", func(ctx *HandlerCtx, p echoParams) (echoResult, error) {
		// A handler declares its OWN type and nothing else - no status, no
		// title, no numeric code. Declarion fills the title.
		return echoResult{}, errs.New("platform.external_service_error").
			WithDetail("ClickUp API 429")
	})
	srv := setupTestServer(t)
	defer srv.Close()

	body := `{"jsonrpc":"2.0","id":"req-1","method":"test.fail","params":{"name":"test"}}`
	resp, err := http.Post(srv.URL+"/rpc", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	var rpcResp Response
	respBody, _ := io.ReadAll(resp.Body)
	require.NoError(t, json.Unmarshal(respBody, &rpcResp))
	assert.Equal(t, JSONRPCAppError, rpcResp.Error.Code)
	assert.Equal(t, "platform.external_service_error", rpcResp.Error.Data.Code())
	assert.Equal(t, "ClickUp API 429", rpcResp.Error.Data.Detail)
	assert.Empty(t, rpcResp.Error.Data.Title,
		"a sidecar carries no title: Declarion renders one from its own declarations, in the caller's language")
}

func TestHandleRPC_invalid_params(t *testing.T) {
	type strictParams struct {
		Count int `json:"count"`
	}
	ClearHandlerRegistry()
	RegisterHandler[strictParams, echoResult]("test.strict", func(ctx *HandlerCtx, p strictParams) (echoResult, error) {
		return echoResult{Message: "ok"}, nil
	})
	srv := setupTestServer(t)
	defer srv.Close()

	body := `{"jsonrpc":"2.0","id":"req-1","method":"test.strict","params":{"count":"not_a_number"}}`
	resp, err := http.Post(srv.URL+"/rpc", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	var rpcResp Response
	respBody, _ := io.ReadAll(resp.Body)
	require.NoError(t, json.Unmarshal(respBody, &rpcResp))
	assert.Equal(t, JSONRPCInvalidParams, rpcResp.Error.Code)
	assert.Equal(t, "action.invalid_params", rpcResp.Error.Data.Code())
}

func TestHandleRPC_invalid_token(t *testing.T) {
	ClearHandlerRegistry()
	RegisterHandler[echoParams, echoResult]("test.echo", func(ctx *HandlerCtx, p echoParams) (echoResult, error) {
		return echoResult{Message: "ok"}, nil
	})
	srv := setupTestServer(t)
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
	assert.Contains(t, []string{"auth.unauthorized", "auth.invalid_token"}, rpcResp.Error.Data.Code())
}

func TestHandleRPC_context_propagation(t *testing.T) {
	var capturedCtx *HandlerCtx
	ClearHandlerRegistry()
	RegisterHandler[echoParams, echoResult]("test.ctx", func(ctx *HandlerCtx, p echoParams) (echoResult, error) {
		capturedCtx = ctx
		return echoResult{Message: "ok"}, nil
	})
	srv := setupTestServer(t)
	defer srv.Close()

	token := mintTestToken(t, "tenant-42", "user-99", "test.ctx", "audit-op-123")
	body := `{"jsonrpc":"2.0","id":"req-1","method":"test.ctx","params":{"name":"test","_entity_code":"lead","_object_ids":["lead-1","lead-2"]}}`
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
	assert.Equal(t, "lead", capturedCtx.EntityCode)
	assert.Equal(t, []string{"lead-1", "lead-2"}, capturedCtx.ObjectIDs)
	assert.Equal(t, "declarion.tenant_id=tenant-42", capturedCtx.Baggage)
	assert.NotNil(t, capturedCtx.Platform)
	assert.NotNil(t, capturedCtx.Logger)
}

func TestHandleRPC_no_token_allowed(t *testing.T) {
	var capturedCtx *HandlerCtx
	ClearHandlerRegistry()
	RegisterHandler[echoParams, echoResult]("test.open", func(ctx *HandlerCtx, p echoParams) (echoResult, error) {
		capturedCtx = ctx
		return echoResult{Message: "ok"}, nil
	})
	srv := setupTestServer(t)
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
	ClearHandlerRegistry()
	RegisterHandler[echoParams, echoResult]("test.echo", func(ctx *HandlerCtx, p echoParams) (echoResult, error) {
		return echoResult{Message: "ok"}, nil
	})
	srv := setupTestServerWithConfig(t, cfg)
	defer srv.Close()

	body := `{"jsonrpc":"2.0","id":"req-1","method":"test.echo","params":{"name":"test"}}`
	resp, err := http.Post(srv.URL+"/rpc", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	var rpcResp Response
	respBody, _ := io.ReadAll(resp.Body)
	require.NoError(t, json.Unmarshal(respBody, &rpcResp))
	assert.Equal(t, JSONRPCAppError, rpcResp.Error.Code)
	assert.Contains(t, []string{"auth.unauthorized", "auth.invalid_token"}, rpcResp.Error.Data.Code())
}

func TestHandleRPC_require_token_allows_authenticated(t *testing.T) {
	cfg := &Config{
		JWTSecret:    testSecret,
		RequireToken: true,
	}
	ClearHandlerRegistry()
	RegisterHandler[echoParams, echoResult]("test.echo", func(ctx *HandlerCtx, p echoParams) (echoResult, error) {
		return echoResult{Message: "hello " + p.Name}, nil
	})
	srv := setupTestServerWithConfig(t, cfg)
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
	ClearHandlerRegistry()
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

// A sidecar carries no catalogue, and must not need one.
//
// It declares a TYPE and the facts that go with it; Declarion resolves the
// title from the declarations IT loaded, in the caller's language. A sidecar
// that shipped its own titles would freeze one language into a second place and
// drift from the first the day a translation was corrected.
func TestASidecarWithNoCatalogueStillAnswersFully(t *testing.T) {
	errs.SetCatalogue(nil, "")
	ClearHandlerRegistry()
	RegisterHandler[echoParams, echoResult]("test.no_catalogue", func(ctx *HandlerCtx, p echoParams) (echoResult, error) {
		return echoResult{}, errs.New("acme-portal.quota_exhausted", errs.Args{"limit": 100}).
			WithDetail("the daily quota is spent")
	})
	srv := setupTestServer(t)
	defer srv.Close()

	body := `{"jsonrpc":"2.0","id":"req-1","method":"test.no_catalogue","params":{"name":"x"}}`
	resp, err := http.Post(srv.URL+"/rpc", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	var rpcResp Response
	respBody, _ := io.ReadAll(resp.Body)
	require.NoError(t, json.Unmarshal(respBody, &rpcResp))
	require.NotNil(t, rpcResp.Error)
	require.NotNil(t, rpcResp.Error.Data)

	assert.Equal(t, "acme-portal.quota_exhausted", rpcResp.Error.Data.Code(),
		"the handler's own type, whether or not this process can name it")
	assert.Equal(t, "the daily quota is spent", rpcResp.Error.Data.Detail)
	assert.Empty(t, rpcResp.Error.Data.Title, "the title is Declarion's to fill")
	assert.Zero(t, rpcResp.Error.Data.Status, "a status describes a boundary this handler is not at")
	limit, ok := rpcResp.Error.Data.ExtInt("limit")
	assert.True(t, ok)
	assert.Equal(t, 100, limit, "a declared member reaches the platform as a member")
}
