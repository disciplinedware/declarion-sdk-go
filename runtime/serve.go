package runtime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/disciplinedware/declarion-sdk-go/errs"

	kern "github.com/disciplinedware/declarion-sdk-go/dispatch"
	"github.com/disciplinedware/declarion-sdk-go/platform"
)

// RunAsTokenHeader carries the optional Core-minted run-as service-user
// credential for a verifier call. It is separate from the powerless verifier
// call token in the Authorization header; the verifier's Platform client is
// built ONLY from this header, never from the call token.
const RunAsTokenHeader = "X-Declarion-Run-As-Token"

const (
	// ProtocolVersion is the Declarion wire contract version this SDK supports.
	ProtocolVersion = "1"

	// MaxRequestSize caps inbound JSON-RPC request bodies (request-in, sidecar).
	// Bulk handlers (e.g. ClickUp imports, CRM bulk upserts) receive previous_result
	// from composites that can legitimately be tens of MB. 100MB matches
	// declarion-core's DefaultHandlerResponseLimit so any payload accepted by the
	// dispatcher can always reach the sidecar.
	//
	// Enforced via http.MaxBytesReader so oversized requests fail loudly with
	// JSON-RPC JSONRPCParseError + a specific message, instead of being silently
	// truncated into a misleading "invalid JSON" parse error.
	MaxRequestSize int64 = 100 * 1024 * 1024
)

// Config configures the sidecar server.
type Config struct {
	// Addr is the listen address (default ":8080").
	Addr string

	// PlatformURL is the base URL of the Declarion platform API (e.g. "http://declarion:3000").
	// Required for ctx.Platform to work. Read from DECLARION_PLATFORM_URL env if empty.
	PlatformURL string

	// JWTSecret is the shared JWT signing key for verifying continuation tokens.
	// When empty, tokens are decoded without signature verification (trusts network boundary).
	// Read from DECLARION_JWT_SECRET env if empty.
	JWTSecret string

	// RequireToken rejects requests without a valid Authorization header.
	// When false (default), requests without tokens succeed with empty identity fields.
	RequireToken bool

	// Logger overrides the default structured logger. Defaults to a zap
	// production logger; tests can pass zaptest or zap.NewNop().
	Logger *zap.Logger

	// ShutdownTimeout is the graceful shutdown deadline (default 10s).
	ShutdownTimeout time.Duration
}

func (c *Config) withDefaults() {
	if c.Addr == "" {
		if addr := os.Getenv("DECLARION_SIDECAR_ADDR"); addr != "" {
			c.Addr = addr
		} else {
			c.Addr = ":8080"
		}
	}
	if c.PlatformURL == "" {
		c.PlatformURL = os.Getenv("DECLARION_PLATFORM_URL")
	}
	if c.JWTSecret == "" {
		c.JWTSecret = os.Getenv("DECLARION_JWT_SECRET")
	}
	if c.Logger == nil {
		// Production-grade JSON logger. Sidecar callers running under
		// systemd / k8s expect structured fields, not plain text. tests
		// inject zap.NewNop() / zaptest.NewLogger.
		l, err := zap.NewProduction()
		if err != nil {
			// zap.NewProduction can fail only on a malformed
			// hard-coded config; surface the misconfiguration rather
			// than swallow it via a Nop fallback that would hide every
			// runtime warning the sidecar emits.
			panic(fmt.Sprintf("declarion-sdk: build production zap logger: %v", err))
		}
		c.Logger = l
	}
	if c.ShutdownTimeout == 0 {
		c.ShutdownTimeout = 10 * time.Second
	}
}

// Serve starts the JSON-RPC sidecar server using every function registered
// via RegisterHandler. Walks the same package-level handlerRegistry that
// GenerateFunctionsYAML consumes — single source of truth for both the
// runtime dispatch table and the YAML manifest. Callers register functions
// in init() of their handler packages (typically through a project-specific
// wrapper that delegates to RegisterHandler). Blocks until SIGTERM/SIGINT,
// then gracefully shuts down.
func Serve(cfg Config) error {
	cfg.withDefaults()

	// Startup gates. Missing JWT secret is fatal when token verification
	// is required: accepting unverified continuation tokens would let any
	// caller mint an arbitrary identity and reach handlers as that user.
	// Tests that need the unverified-parse path can leave RequireToken=false
	// AND set DECLARION_SIDECAR_ALLOW_UNVERIFIED=1 explicitly.
	if cfg.JWTSecret == "" {
		if cfg.RequireToken {
			return fmt.Errorf("DECLARION_JWT_SECRET is required when RequireToken=true; refusing to start with unverified token parsing")
		}
		if os.Getenv("DECLARION_SIDECAR_ALLOW_UNVERIFIED") != "1" {
			return fmt.Errorf("DECLARION_JWT_SECRET is empty; set the env var or DECLARION_SIDECAR_ALLOW_UNVERIFIED=1 (test-only) to enable unverified token parsing")
		}
		cfg.Logger.Warn("DECLARION_JWT_SECRET empty and DECLARION_SIDECAR_ALLOW_UNVERIFIED=1: continuation tokens parsed without signature verification (test-only mode; do not use in production)")
	}
	if cfg.PlatformURL == "" {
		cfg.Logger.Warn("DECLARION_PLATFORM_URL not set: ctx.Platform calls will fail")
	}

	// A deployment that registers any verifier MUST enable signed-token
	// verification: verifier calls carry a signed verifier-only token and the
	// SDK never serves a verifier method unsigned. Fail closed at boot rather
	// than accept anonymous pre-authentication calls.
	if registeredVerifierCount() > 0 && cfg.JWTSecret == "" {
		return fmt.Errorf("DECLARION_JWT_SECRET is required when verifiers are registered; verifier methods are never served without signature verification")
	}

	registeredCount := registeredHandlerCount()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /rpc", func(w http.ResponseWriter, r *http.Request) {
		handleRPC(w, r, &cfg)
	})
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Start listening.
	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", cfg.Addr, err)
	}

	cfg.Logger.Info("sidecar starting",
		zap.String("addr", cfg.Addr),
		zap.Int("handlers", registeredCount),
		zap.Int("verifiers", registeredVerifierCount()),
	)

	// Graceful shutdown on SIGTERM/SIGINT.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	errCh := make(chan error, 1)
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case sig := <-sigCh:
		cfg.Logger.Info("received signal, shutting down", zap.Stringer("signal", sig))
	case err := <-errCh:
		if err != nil {
			return err
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}

	cfg.Logger.Info("sidecar stopped")
	return nil
}

func handleRPC(w http.ResponseWriter, r *http.Request, cfg *Config) {
	w.Header().Set("Content-Type", "application/json")

	// Read and parse the request body FIRST so we have req.ID for error responses.
	// http.MaxBytesReader rejects oversized requests loudly with *http.MaxBytesError,
	// which we surface as JSONRPCParseError + specific message rather than a
	// silent truncation that would look like an "invalid JSON" error downstream.
	r.Body = http.MaxBytesReader(w, r.Body, MaxRequestSize)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeJSON(w, http.StatusOK, NewErrorResponse("", JSONRPCParseError,
				errs.New("platform.body_too_large", errs.Args{"threshold": MaxRequestSize}).Because(err)))
			return
		}
		writeJSON(w, http.StatusOK, NewErrorResponse("", JSONRPCParseError, errs.New("platform.read_body_failed").Because(err)))
		return
	}

	var req Request
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusOK, NewErrorResponse("", JSONRPCParseError, errs.New("platform.invalid_body").Because(err)))
		return
	}

	// Check protocol version (now req.ID is available for error correlation).
	protoVer := r.Header.Get("X-Declarion-Protocol-Version")
	if protoVer != "" && protoVer != ProtocolVersion {
		writeJSON(w, http.StatusOK, NewErrorResponse(req.ID, JSONRPCServerError,
			errs.New("handler.protocol_mismatch", errs.Args{"expected": ProtocolVersion, "got": protoVer})))
		return
	}

	if req.JSONRPC != "2.0" {
		writeJSON(w, http.StatusOK, NewErrorResponse(req.ID, JSONRPCInvalidRequest, errs.New("platform.invalid_body_shape")))
		return
	}

	// Extract continuation token from Authorization header.
	token := extractBearer(r.Header.Get("Authorization"))
	traceparent := r.Header.Get("traceparent")
	baggage := r.Header.Get("baggage")

	// Route by validated token family. A verifier-dispatch token selects the
	// verifier registry; the two registries are disjoint even when a code
	// string collides, so a handler token can never reach a verifier and a
	// verifier token can never reach a handler (registry-miss -> method-not-found).
	if token != "" {
		if aud, audErr := tokenAudience(token); audErr == nil && aud == VerifierTokenAudience {
			handleVerifierDispatch(w, r, cfg, &req, token)
			return
		}
	}

	// Enforce RequireToken: reject requests without a valid bearer token.
	if cfg.RequireToken && token == "" {
		writeJSON(w, http.StatusOK, NewErrorResponse(req.ID, JSONRPCServerError, errs.New("auth.unauthorized")))
		return
	}

	// Parse claims from token (identity + authority extraction).
	var (
		tenantID, tenantCode, userID, auditOp, action string
		permissions                                   []string
		isSuperadmin, isTenantOwner, isGlobalUser     bool
	)
	if token != "" {
		claims, err := parseHandlerToken(token, cfg.JWTSecret)
		if err != nil {
			cfg.Logger.Warn("invalid continuation token", zap.Error(err), zap.String("method", req.Method))
			writeJSON(w, http.StatusOK, NewErrorResponse(req.ID, JSONRPCServerError, errs.New("auth.invalid_token")))
			return
		}
		// Exact-method binding (defense-in-depth): a token minted for one
		// method cannot be replayed on another. Tolerated empty during rollout
		// (older Core mints omit the method claim).
		if claims.Method != "" && claims.Method != req.Method {
			cfg.Logger.Warn("handler token method mismatch", zap.String("claim_method", claims.Method), zap.String("method", req.Method))
			writeJSON(w, http.StatusOK, NewErrorResponse(req.ID, JSONRPCServerError, errs.New("auth.invalid_token")))
			return
		}
		tenantID = claims.TenantID
		tenantCode = claims.TenantCode
		userID = claims.UserID
		auditOp = claims.AuditOpID
		action = claims.Action
		permissions = claims.Permissions
		isSuperadmin = claims.IsSuperadmin
		isTenantOwner = claims.IsTenantOwner
		isGlobalUser = claims.IsGlobalUser
	}

	// Build platform client.
	platClient := platform.New(platform.Config{
		BaseURL:     cfg.PlatformURL,
		Token:       token,
		Traceparent: traceparent,
		Baggage:     baggage,
	})

	// Extract reserved keys from JSON-RPC params before the handler's typed
	// params are unmarshalled. Reserved keys (underscore prefix) are
	// platform-injected metadata; handlers see them via dedicated HandlerCtx
	// fields, not as part of their declared params surface.
	reserved, paramsWithoutReserved, paramsErr := extractReservedParams(req.Params)
	if paramsErr != nil {
		writeJSON(w, http.StatusOK, NewErrorResponse(req.ID, JSONRPCInvalidParams, paramsErr))
		return
	}

	// Build handler context.
	hctx := &HandlerCtx{
		Context:  r.Context(),
		Platform: platClient,
		Logger: cfg.Logger.With(
			zap.String("method", req.Method),
			zap.String("tenant_id", tenantID),
			zap.String("user_id", userID),
			zap.String("audit_op", auditOp),
		),
		TenantID:      tenantID,
		TenantCode:    tenantCode,
		UserID:        userID,
		AuditOp:       auditOp,
		Action:        action,
		Permissions:   permissions,
		IsSuperadmin:  isSuperadmin,
		IsTenantOwner: isTenantOwner,
		IsGlobalUser:  isGlobalUser,
		EntityCode:    reserved.EntityCode,
		ObjectIDs:     reserved.ObjectIDs,
		Locale:        reserved.Locale,
		Baggage:       baggage,
	}

	// Dispatch with params stripped of reserved keys.
	result, err := executeRegisteredHandler(req.Method, hctx, paramsWithoutReserved)
	if err != nil {
		writeHandlerError(w, req.ID, req.Method, err, cfg)
		return
	}

	writeJSON(w, http.StatusOK, NewResultResponse(req.ID, result))
}

// handleVerifierDispatch serves a verifier method under the verifier-only token
// audience. Verifier methods ALWAYS require a signed token (no unsigned/test
// bypass), the token authorizes exactly one method, and the Platform client is
// built ONLY from an optional run-as credential header - never the call token.
func handleVerifierDispatch(w http.ResponseWriter, r *http.Request, cfg *Config, req *Request, token string) {
	if cfg.JWTSecret == "" {
		writeJSON(w, http.StatusOK, NewErrorResponse(req.ID, JSONRPCServerError,
			errs.New("auth.unauthorized")))
		return
	}
	claims, err := parseVerifierToken(token, cfg.JWTSecret)
	if err != nil {
		cfg.Logger.Warn("invalid verifier token", zap.Error(err), zap.String("method", req.Method))
		writeJSON(w, http.StatusOK, NewErrorResponse(req.ID, JSONRPCServerError,
			errs.New("auth.invalid_token")))
		return
	}
	// Exact-method binding before registry lookup: the token authorizes exactly
	// one verifier method.
	if claims.Method != req.Method {
		cfg.Logger.Warn("verifier token method mismatch", zap.String("claim_method", claims.Method), zap.String("method", req.Method))
		writeJSON(w, http.StatusOK, NewErrorResponse(req.ID, JSONRPCServerError,
			errs.New("auth.invalid_token")))
		return
	}
	fn, ok := lookupVerifier(req.Method)
	if !ok {
		writeJSON(w, http.StatusOK, NewErrorResponse(req.ID, JSONRPCMethodNotFound,
			errs.New("handler.not_registered", errs.Args{"method": req.Method})))
		return
	}
	env, err := decodeExternalRequestEnvelope(req.Params)
	if err != nil {
		writeJSON(w, http.StatusOK, NewErrorResponse(req.ID, JSONRPCInvalidParams,
			errs.New("action.invalid_params").Because(err)))
		return
	}
	rawBody, err := base64.StdEncoding.DecodeString(env.RawBodyBase64)
	if err != nil {
		writeJSON(w, http.StatusOK, NewErrorResponse(req.ID, JSONRPCInvalidParams,
			errs.New("action.invalid_params", errs.Args{"param": "raw_body_base64"}).Because(err)))
		return
	}

	// Platform client ONLY from the run-as credential, never the verifier token.
	var platClient *platform.Client
	runAs := r.Header.Get(RunAsTokenHeader)
	if runAs != "" {
		platClient = platform.New(platform.Config{
			BaseURL:     cfg.PlatformURL,
			Token:       runAs,
			Traceparent: r.Header.Get("traceparent"),
			Baggage:     r.Header.Get("baggage"),
		})
	}

	vctx := &VerifierCtx{
		Context:       r.Context(),
		Logger:        cfg.Logger.With(zap.String("verifier", req.Method), zap.String("action", claims.Action)),
		ActionCode:    env.ActionCode,
		VerifierCode:  env.VerifierCode,
		HTTPMethod:    env.HTTPMethod,
		Path:          env.Path,
		PathValues:    env.PathValues,
		Query:         env.Query,
		Headers:       env.Headers,
		RawBody:       rawBody,
		RequestID:     env.RequestID,
		RemoteAddress: env.RemoteAddress,
		Platform:      platClient,
		runAs:         runAs,
		platformURL:   cfg.PlatformURL,
	}

	result, err := fn(vctx)
	if err != nil {
		writeVerifierError(w, req.ID, req.Method, err, cfg)
		return
	}
	writeJSON(w, http.StatusOK, NewResultResponse(req.ID, result))
}

// writeVerifierError renders a verifier's decline onto the JSON-RPC error
// envelope with its outcome class. An error that is NOT a *VerifierError (a
// verifier bug, a leaked infrastructure error) deliberately renders as
// unavailable rather than as a rejection: Core must not turn an internal fault
// into a permanent 401 that makes the provider drop the delivery.
func writeVerifierError(w http.ResponseWriter, id, method string, err error, cfg *Config) {
	var vErr *VerifierError
	if errors.As(err, &vErr) {
		wireErr, rpc := vErr.wire()
		// The reason is logged HERE and nowhere else: it is internal telemetry
		// one hop from a public webhook response.
		cfg.Logger.Info("verifier declined request",
			zap.String("verifier", method),
			zap.String("outcome", string(vErr.Outcome)),
			zap.String("reason", vErr.Reason))
		writeJSON(w, http.StatusOK, NewErrorResponse(id, rpc, wireErr))
		return
	}
	cfg.Logger.Error("verifier failed", zap.String("verifier", method), zap.Error(err))
	writeJSON(w, http.StatusOK, NewErrorResponse(id, JSONRPCInternalError,
		errs.New(CodeVerifierUnavailable).Because(err)))
}

// externalRequestEnvelope is the closed `_external_request` wire envelope Core
// sends to a verifier. Mirrors declarion-core engine.VerifierRequest JSON.
type externalRequestEnvelope struct {
	ActionCode    string              `json:"action_code"`
	VerifierCode  string              `json:"verifier_code"`
	HTTPMethod    string              `json:"http_method"`
	Path          string              `json:"path"`
	PathValues    map[string]string   `json:"path_values"`
	Query         map[string][]string `json:"query"`
	Headers       map[string][]string `json:"headers"`
	RawBodyBase64 string              `json:"raw_body_base64"`
	RequestID     string              `json:"request_id"`
	RemoteAddress string              `json:"remote_address"`
}

// decodeExternalRequestEnvelope extracts the single reserved `_external_request`
// envelope from the JSON-RPC params. The envelope is closed: provider data
// lives in raw_body and allowlisted query/header values, never at top level.
func decodeExternalRequestEnvelope(raw json.RawMessage) (*externalRequestEnvelope, error) {
	var bag struct {
		Env *externalRequestEnvelope `json:"_external_request"`
	}
	if err := json.Unmarshal(raw, &bag); err != nil {
		return nil, fmt.Errorf("invalid verifier params: %w", err)
	}
	if bag.Env == nil {
		return nil, fmt.Errorf("missing _external_request envelope")
	}
	return bag.Env, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func extractBearer(auth string) string {
	if len(auth) > 7 && strings.EqualFold(auth[:7], "bearer ") {
		return auth[7:]
	}
	return ""
}

// writeHandlerError renders what a handler returned.
//
// A handler's OWN type passes through untouched - identity surviving the hop is
// the whole point, and Declarion fills the title from its own declarations, so
// a sidecar needs no catalogue. Everything else takes a declared type here,
// with the original as the logged cause: an unrecognised Go error's text is not
// a vetted sentence and does not belong on a wire.
func writeHandlerError(w http.ResponseWriter, id, method string, err error, cfg *Config) {
	if e, ok := errs.From(err); ok {
		writeJSON(w, http.StatusOK, NewErrorResponse(id, JSONRPCCodeFor(e), e))
		return
	}
	if errors.Is(err, kern.ErrNotFound) {
		writeJSON(w, http.StatusOK, NewErrorResponse(id, JSONRPCMethodNotFound,
			errs.New("handler.not_registered", errs.Args{"method": method})))
		return
	}
	cfg.Logger.Error("handler error", zap.String("method", method), zap.Error(err))
	writeJSON(w, http.StatusOK, NewErrorResponse(id, JSONRPCInternalError,
		errs.New(errs.CodeInternalError).Because(err)))
}

// reservedParams holds the platform-injected metadata carried on JSON-RPC
// params under reserved (`_`-prefixed) keys. These reach handlers only through
// dedicated read-only HandlerCtx fields, never their typed param surface.
type reservedParams struct {
	EntityCode string
	ObjectIDs  []string
	Locale     string
}

// extractReservedParams pulls platform-reserved metadata from JSON-RPC params
// and returns it plus the params with those keys removed. Non-object params pass
// through untouched. Any UNKNOWN `_`-prefixed key is rejected (fail closed) so
// a caller cannot smuggle spoofed reserved metadata past the typed handler
// surface (business params never use the `_` prefix by convention).
func extractReservedParams(raw json.RawMessage) (reservedParams, json.RawMessage, *errs.Error) {
	var out reservedParams
	if len(raw) == 0 {
		return out, raw, nil
	}
	var bag map[string]json.RawMessage
	if err := json.Unmarshal(raw, &bag); err != nil {
		return out, raw, nil
	}
	unmarshalReserved := func(key string, dst any) *errs.Error {
		v, ok := bag[key]
		if !ok {
			return nil
		}
		delete(bag, key)
		if err := json.Unmarshal(v, dst); err != nil {
			// The KEY, never the value: the sender must be able to fix the
			// call, and their value may be anything.
			return errs.New("action.invalid_params", errs.Args{"param": key}).Because(err)
		}
		return nil
	}
	if err := unmarshalReserved("_entity_code", &out.EntityCode); err != nil {
		return out, raw, err
	}
	if err := unmarshalReserved("_object_ids", &out.ObjectIDs); err != nil {
		return out, raw, err
	}
	if err := unmarshalReserved("_locale", &out.Locale); err != nil {
		return out, raw, err
	}
	// Fail closed on any remaining reserved-prefixed key.
	for k := range bag {
		if strings.HasPrefix(k, "_") {
			return out, raw, errs.New("action.invalid_params", errs.Args{"param": k})
		}
	}
	cleaned, err := json.Marshal(bag)
	if err != nil {
		return out, raw, errs.New("platform.internal_error").Because(err)
	}
	return out, cleaned, nil
}
