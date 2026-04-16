// Package conformance provides a language-agnostic test harness for Declarion
// JSON-RPC sidecars. It acts as a fake platform: mints continuation tokens,
// sends JSON-RPC requests to the sidecar under test, intercepts callbacks,
// and asserts header propagation, token handling, error shapes, and callback auth.
package conformance

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/disciplinedware/declarion-sdk-go/runtime"
)

const jwtSecret = "conformance-test-secret"

// TestResult records the outcome of a single conformance test.
type TestResult struct {
	Name   string
	Passed bool
	Error  string
}

// Harness is the conformance test harness.
type Harness struct {
	// SidecarURL is the base URL of the sidecar under test (e.g. "http://localhost:8080").
	SidecarURL string

	// CallbackRecords captures callbacks the sidecar made to the fake platform.
	CallbackRecords []CallbackRecord

	results []TestResult
	fakeAPI *httptest.Server
}

// CallbackRecord captures a single callback from the sidecar to the fake platform.
type CallbackRecord struct {
	Method      string
	Path        string
	AuthHeader  string
	Traceparent string
	Baggage     string
	Body        []byte
}

// NewHarness creates a harness targeting the given sidecar URL.
func NewHarness(sidecarURL string) *Harness {
	return &Harness{SidecarURL: strings.TrimRight(sidecarURL, "/")}
}

// RunAll executes all conformance tests and returns the results.
func (h *Harness) RunAll() []TestResult {
	return h.RunAllWithSidecarConfig(nil)
}

// RunAllWithSidecarConfig runs all conformance tests. If cfg is non-nil, sets
// PlatformURL on it to point at the harness's fake platform API so ctx.Platform
// callbacks work for in-process testing.
func (h *Harness) RunAllWithSidecarConfig(cfg *runtime.Config) []TestResult {
	h.results = nil

	// Start the fake platform API to capture callbacks.
	h.startFakeAPI()
	defer h.fakeAPI.Close()

	// If a sidecar config was provided, set PlatformURL to the fake API so
	// ctx.Platform.Data().Create() hits the harness's callback recorder.
	if cfg != nil {
		cfg.SetPlatformURL(h.fakeAPI.URL)
	}

	h.testSuccessResponse()
	h.testErrorResponse()
	h.testHeaderPropagation()
	h.testTokenForwarding()
	h.testBaggagePropagation()
	h.testInvalidTokenRejection()
	h.testProtocolVersionMismatch()
	h.testMethodNotFound()

	return h.results
}

func (h *Harness) startFakeAPI() {
	h.CallbackRecords = nil
	h.fakeAPI = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		h.CallbackRecords = append(h.CallbackRecords, CallbackRecord{
			Method:      r.Method,
			Path:        r.URL.Path,
			AuthHeader:  r.Header.Get("Authorization"),
			Traceparent: r.Header.Get("traceparent"),
			Baggage:     r.Header.Get("baggage"),
			Body:        body,
		})
		// Return a generic success response.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`[{"id":"fake-1","status":"ok"}]`))
	}))
}

func (h *Harness) record(name string, err error) {
	if err != nil {
		h.results = append(h.results, TestResult{Name: name, Passed: false, Error: err.Error()})
	} else {
		h.results = append(h.results, TestResult{Name: name, Passed: true})
	}
}

func (h *Harness) mintToken(tenantID, userID, action, auditOp string) string {
	claims := &runtime.HandlerClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "declarion",
			Subject:   userID,
			Audience:  jwt.ClaimStrings{runtime.HandlerTokenAudience},
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(10 * time.Minute)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ID:        fmt.Sprintf("conf-%d", time.Now().UnixNano()),
		},
		UserID:     userID,
		TenantID:   tenantID,
		TenantCode: "test-tenant",
		Action:     action,
		AuditOpID:  auditOp,
		Scope:      runtime.HandlerTokenScope,
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, _ := token.SignedString([]byte(jwtSecret))
	return signed
}

type jsonrpcReq struct {
	JSONRPC string `json:"jsonrpc"`
	ID      string `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
}

type jsonrpcResp struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      string          `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *jsonrpcErr     `json:"error,omitempty"`
}

type jsonrpcErr struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (h *Harness) callSidecar(method string, params any, token, traceparent, protoVer string) (*jsonrpcResp, http.Header, error) {
	return h.callSidecarWithBaggage(method, params, token, traceparent, "", protoVer)
}

func (h *Harness) callSidecarWithBaggage(method string, params any, token, traceparent, baggage, protoVer string) (*jsonrpcResp, http.Header, error) {
	envelope := jsonrpcReq{JSONRPC: "2.0", ID: "conf-1", Method: method, Params: params}
	body, _ := json.Marshal(envelope)

	req, err := http.NewRequest("POST", h.SidecarURL+"/rpc", bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if traceparent != "" {
		req.Header.Set("traceparent", traceparent)
	}
	if baggage != "" {
		req.Header.Set("baggage", baggage)
	}
	if protoVer != "" {
		req.Header.Set("X-Declarion-Protocol-Version", protoVer)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, _ := io.ReadAll(resp.Body)
	var rpcResp jsonrpcResp
	if err := json.Unmarshal(respBody, &rpcResp); err != nil {
		return nil, resp.Header, fmt.Errorf("invalid JSON response: %s", string(respBody))
	}

	return &rpcResp, resp.Header, nil
}

// --- Individual tests ---

// testSuccessResponse: call conformance.echo, expect a result with the echoed name.
func (h *Harness) testSuccessResponse() {
	token := h.mintToken("t1", "u1", "conformance.echo", "op1")
	params := map[string]any{"name": "conformance"}
	resp, _, err := h.callSidecar("conformance.echo", params, token, "", runtime.ProtocolVersion)
	if err != nil {
		h.record("success_response", err)
		return
	}
	if resp.Error != nil {
		h.record("success_response", fmt.Errorf("expected result, got error: %s", resp.Error.Message))
		return
	}
	var result map[string]any
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		h.record("success_response", fmt.Errorf("unmarshal result: %w", err))
		return
	}
	msg, _ := result["message"].(string)
	if !strings.Contains(msg, "conformance") {
		h.record("success_response", fmt.Errorf("expected message containing 'conformance', got %q", msg))
		return
	}
	h.record("success_response", nil)
}

// testErrorResponse: call conformance.error, expect a structured JSON-RPC error.
func (h *Harness) testErrorResponse() {
	token := h.mintToken("t1", "u1", "conformance.error", "op2")
	resp, _, err := h.callSidecar("conformance.error", map[string]any{}, token, "", runtime.ProtocolVersion)
	if err != nil {
		h.record("error_response", err)
		return
	}
	if resp.Error == nil {
		h.record("error_response", fmt.Errorf("expected error, got result"))
		return
	}
	if resp.Error.Code >= 0 {
		h.record("error_response", fmt.Errorf("expected negative error code, got %d", resp.Error.Code))
		return
	}
	h.record("error_response", nil)
}

// testHeaderPropagation: send traceparent, verify the sidecar's callback includes it.
func (h *Harness) testHeaderPropagation() {
	token := h.mintToken("t1", "u1", "conformance.callback", "op3")
	traceparent := "00-abcdef1234567890abcdef1234567890-1234567890abcdef-01"
	params := map[string]any{"callback_url": h.fakeAPI.URL + "/api/data/test"}
	resp, _, err := h.callSidecar("conformance.callback", params, token, traceparent, runtime.ProtocolVersion)
	if err != nil {
		h.record("header_propagation", err)
		return
	}
	if resp.Error != nil {
		h.record("header_propagation", fmt.Errorf("handler error: %s", resp.Error.Message))
		return
	}

	// Check that the callback was received and traceparent was forwarded.
	found := false
	for _, cb := range h.CallbackRecords {
		if cb.Traceparent == traceparent {
			found = true
			break
		}
	}
	if !found {
		h.record("header_propagation", fmt.Errorf("traceparent not forwarded in callbacks (got %d callbacks)", len(h.CallbackRecords)))
		return
	}
	h.record("header_propagation", nil)
}

// testTokenForwarding: verify the sidecar forwards the bearer token on callbacks.
func (h *Harness) testTokenForwarding() {
	token := h.mintToken("t1", "u1", "conformance.callback", "op4")
	params := map[string]any{"callback_url": h.fakeAPI.URL + "/api/data/test"}
	h.CallbackRecords = nil
	resp, _, err := h.callSidecar("conformance.callback", params, token, "", runtime.ProtocolVersion)
	if err != nil {
		h.record("token_forwarding", err)
		return
	}
	if resp.Error != nil {
		h.record("token_forwarding", fmt.Errorf("handler error: %s", resp.Error.Message))
		return
	}

	found := false
	for _, cb := range h.CallbackRecords {
		if cb.AuthHeader == "Bearer "+token {
			found = true
			break
		}
	}
	if !found {
		var auths []string
		for _, cb := range h.CallbackRecords {
			auths = append(auths, cb.AuthHeader)
		}
		h.record("token_forwarding", fmt.Errorf("bearer token not forwarded in callbacks (got auths: %v)", auths))
		return
	}
	h.record("token_forwarding", nil)
}

// testBaggagePropagation: send baggage, verify the sidecar's callback includes it.
func (h *Harness) testBaggagePropagation() {
	token := h.mintToken("t1", "u1", "conformance.callback", "op5")
	baggage := "declarion.tenant_id=t1,declarion.user_id=u1,declarion.audit_operation_id=op5"
	params := map[string]any{"callback_url": h.fakeAPI.URL + "/api/data/test"}
	h.CallbackRecords = nil
	resp, _, err := h.callSidecarWithBaggage("conformance.callback", params, token, "", baggage, runtime.ProtocolVersion)
	if err != nil {
		h.record("baggage_propagation", err)
		return
	}
	if resp.Error != nil {
		h.record("baggage_propagation", fmt.Errorf("handler error: %s", resp.Error.Message))
		return
	}

	found := false
	for _, cb := range h.CallbackRecords {
		if cb.Baggage == baggage {
			found = true
			break
		}
	}
	if !found {
		h.record("baggage_propagation", fmt.Errorf("baggage not forwarded in callbacks (got %d callbacks)", len(h.CallbackRecords)))
		return
	}
	h.record("baggage_propagation", nil)
}

// testInvalidTokenRejection: send garbage token, expect error.
func (h *Harness) testInvalidTokenRejection() {
	resp, _, err := h.callSidecar("conformance.echo", map[string]any{"name": "test"}, "garbage-token", "", runtime.ProtocolVersion)
	if err != nil {
		h.record("invalid_token_rejection", err)
		return
	}
	if resp.Error == nil {
		h.record("invalid_token_rejection", fmt.Errorf("expected error for invalid token, got success"))
		return
	}
	h.record("invalid_token_rejection", nil)
}

// testProtocolVersionMismatch: send wrong version, expect PROTOCOL_VERSION_MISMATCH.
func (h *Harness) testProtocolVersionMismatch() {
	resp, _, err := h.callSidecar("conformance.echo", map[string]any{"name": "test"}, "", "99", "99")
	if err != nil {
		h.record("protocol_version_mismatch", err)
		return
	}
	if resp.Error == nil {
		h.record("protocol_version_mismatch", fmt.Errorf("expected error for wrong protocol version"))
		return
	}
	h.record("protocol_version_mismatch", nil)
}

// testMethodNotFound: call a nonexistent method.
func (h *Harness) testMethodNotFound() {
	resp, _, err := h.callSidecar("nonexistent.method", map[string]any{}, "", "", runtime.ProtocolVersion)
	if err != nil {
		h.record("method_not_found", err)
		return
	}
	if resp.Error == nil {
		h.record("method_not_found", fmt.Errorf("expected error for unknown method"))
		return
	}
	if resp.Error.Code != -32601 {
		h.record("method_not_found", fmt.Errorf("expected code -32601, got %d", resp.Error.Code))
		return
	}
	h.record("method_not_found", nil)
}
