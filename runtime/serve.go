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
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/disciplinedware/declarion-sdk-go/errs"
	"github.com/disciplinedware/declarion-sdk-go/tracing"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	kern "github.com/disciplinedware/declarion-sdk-go/dispatch"
	"github.com/disciplinedware/declarion-sdk-go/platform"
)

// RunAsTokenHeader carries the optional Core-minted run-as service-user
// credential for a verifier call. It is separate from the powerless verifier
// call token in the Authorization header; the verifier's Platform client is
// built ONLY from this header, never from the call token.
const RunAsTokenHeader = "X-Declarion-Run-As-Token"

// HandlerTimeoutHeader carries the milliseconds the platform will wait for this
// call, counted when it sent it. The handler's context carries the deadline it
// makes, so a handler doing bounded rounds can stop starting one it cannot
// finish; relative, as gRPC's grpc-timeout, so the two clocks need not agree.
const HandlerTimeoutHeader = "X-Declarion-Timeout-Ms"

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

	// PlatformIdleConnTimeout closes a connection to the platform once it has
	// been idle this long: from DECLARION_PLATFORM_IDLE_CONN_TIMEOUT (a Go
	// duration) when zero, else platform.DefaultIdleConnTimeout. It must be below
	// the platform's http_idle_timeout - platform.NewTransport says why.
	PlatformIdleConnTimeout time.Duration

	// platformHTTP is the one client every ctx.Platform shares, built by
	// connectPlatform from PlatformIdleConnTimeout.
	platformHTTP *http.Client

	// JWTSecret is the shared JWT signing key for verifying continuation tokens.
	// When empty, tokens are decoded without signature verification (trusts network boundary).
	// Read from DECLARION_JWT_SECRET env if empty.
	JWTSecret string

	// RequireToken rejects requests without a valid Authorization header.
	// When false (default), requests without tokens succeed with empty identity fields.
	RequireToken bool

	// Authenticator replaces the default HS256 handler-token verifier. It is
	// responsible for refusing unauthenticated requests and may return a
	// callback bearer distinct from the credential it verifies.
	Authenticator RequestAuthenticator

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

const envPlatformIdleConnTimeout = "DECLARION_PLATFORM_IDLE_CONN_TIMEOUT"

// connectPlatform builds the client every ctx.Platform shares: the declared idle
// timeout, else platform.DefaultIdleConnTimeout. An unparsable value is refused.
func (c *Config) connectPlatform() error {
	if c.PlatformIdleConnTimeout == 0 {
		raw := os.Getenv(envPlatformIdleConnTimeout)
		if raw == "" {
			raw = platform.DefaultIdleConnTimeout.String()
		}
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			return fmt.Errorf("%s=%q is not a duration: %w", envPlatformIdleConnTimeout, raw, err)
		}
		c.PlatformIdleConnTimeout = parsed
	}
	transport, err := platform.NewTransport(c.PlatformIdleConnTimeout)
	if err != nil {
		return fmt.Errorf("%s: %w", envPlatformIdleConnTimeout, err)
	}
	c.platformHTTP = &http.Client{Transport: transport}
	return nil
}

// Serve starts the JSON-RPC sidecar server using every function registered
// via RegisterHandler. Walks the same package-level handlerRegistry that
// GenerateFunctionsYAML consumes — single source of truth for both the
// runtime dispatch table and the YAML manifest. Callers register functions
// in init() of their handler packages (typically through a project-specific
// wrapper that delegates to RegisterHandler). Blocks until SIGTERM/SIGINT,
// then gracefully shuts down.
func Serve(cfg Config) error {
	handler, err := NewHandler(cfg)
	if err != nil {
		return err
	}
	cfg.withDefaults()
	mux := http.NewServeMux()
	mux.Handle("POST /rpc", handler)
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
		zap.Int("handlers", registeredHandlerCount()),
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

// NewHandler returns the authenticated JSON-RPC callback handler for a host
// that owns its own HTTP listener. It applies the same startup checks as Serve;
// callers must not reimplement the request framing or authentication gate.
func NewHandler(cfg Config) (http.Handler, error) {
	cfg.withDefaults()

	// Startup gates. Missing JWT secret is fatal when token verification
	// is required: accepting unverified continuation tokens would let any
	// caller mint an arbitrary identity and reach handlers as that user.
	// Tests that need the unverified-parse path can leave RequireToken=false
	// AND set DECLARION_SIDECAR_ALLOW_UNVERIFIED=1 explicitly.
	if cfg.Authenticator == nil && cfg.JWTSecret == "" {
		if cfg.RequireToken {
			return nil, fmt.Errorf("DECLARION_JWT_SECRET is required when RequireToken=true; refusing to start with unverified token parsing")
		}
		if os.Getenv("DECLARION_SIDECAR_ALLOW_UNVERIFIED") != "1" {
			return nil, fmt.Errorf("DECLARION_JWT_SECRET is empty; set the env var or DECLARION_SIDECAR_ALLOW_UNVERIFIED=1 (test-only) to enable unverified token parsing")
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
		return nil, fmt.Errorf("DECLARION_JWT_SECRET is required when verifiers are registered; verifier methods are never served without signature verification")
	}
	if err := cfg.connectPlatform(); err != nil {
		return nil, err
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handleRPC(w, r, &cfg)
	}), nil
}

func handleRPC(w http.ResponseWriter, r *http.Request, cfg *Config) {
	ctx := tracing.WithWork(tracing.ExtractTrace(r.Context(), r.Header), "", "http:"+r.Method)
	ctx, span := otel.Tracer(tracing.InstrumentationName).Start(ctx, "rpc", trace.WithSpanKind(trace.SpanKindServer), trace.WithAttributes(attribute.String("rpc.system.name", "jsonrpc"), attribute.String("jsonrpc.protocol.version", "2.0")))
	defer span.End()
	r = r.WithContext(ctx)
	requestConfig := *cfg
	requestConfig.Logger = tracing.Logger(ctx, cfg.Logger)
	cfg = &requestConfig
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
			writeError(ctx, w, cfg.Logger, "", JSONRPCParseError,
				errs.New("platform.body_too_large", errs.Args{"threshold": MaxRequestSize}).Because(err))
			return
		}
		writeError(ctx, w, cfg.Logger, "", JSONRPCParseError, errs.New("platform.read_body_failed").Because(err))
		return
	}

	var req Request
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(ctx, w, cfg.Logger, "", JSONRPCParseError, errs.New("platform.invalid_body").Because(err))
		return
	}
	_, verifierRegistered := lookupVerifier(req.Method)
	if hasRegisteredHandler(req.Method) || verifierRegistered {
		span.SetName(req.Method)
		span.SetAttributes(attribute.String("rpc.method", req.Method))
	}
	log := cfg.Logger.With(zap.String("method", req.Method))

	// Check protocol version (now req.ID is available for error correlation).
	protoVer := r.Header.Get("X-Declarion-Protocol-Version")
	if protoVer != "" && protoVer != ProtocolVersion {
		writeError(ctx, w, log, req.ID, JSONRPCServerError,
			errs.New("handler.protocol_mismatch", errs.Args{"expected": ProtocolVersion, "got": protoVer}))
		return
	}

	if req.JSONRPC != "2.0" {
		writeError(ctx, w, log, req.ID, JSONRPCInvalidRequest, errs.New("platform.invalid_body_shape"))
		return
	}

	// Extract continuation token from Authorization header.
	token := extractBearer(r.Header.Get("Authorization"))

	// Route by validated token family. A verifier-dispatch token selects the
	// verifier registry; the two registries are disjoint even when a code
	// string collides, so a handler token can never reach a verifier and a
	// verifier token can never reach a handler (registry-miss -> method-not-found).
	if token != "" {
		if aud, audErr := tokenAudience(token); audErr == nil && aud == VerifierTokenAudience {
			handleVerifierDispatch(w, r, cfg, log, &req, token)
			return
		}
	}

	verified, err := cfg.authenticateRequest(r, req.Method)
	if err != nil {
		writeError(ctx, w, log, req.ID, JSONRPCServerError, errs.New("auth.invalid_token").Because(err))
		return
	}

	verified.Context = trace.ContextWithSpan(verified.Context, span)
	verified.Context = tracing.WithWork(verified.Context, "", "http:"+r.Method)
	if token != "" && (cfg.JWTSecret != "" || cfg.Authenticator != nil) {
		verified.Context = tracing.AdoptPath(verified.Context, r.Header)
	}
	verified.Context = handlerTracePath(verified.Context, req.Method)
	log = tracing.Logger(verified.Context, log)
	handlerContext, cancel, timeoutErr := withHandlerTimeout(verified.Context, r.Header.Get(HandlerTimeoutHeader))
	if timeoutErr != nil {
		writeError(ctx, w, log, req.ID, JSONRPCServerError, timeoutErr)
		return
	}
	defer cancel()

	// Build platform client.
	platClient := platform.New(platform.Config{
		BaseURL:    cfg.PlatformURL,
		Token:      verified.PlatformToken,
		HTTPClient: cfg.platformHTTP,
	})

	// Extract reserved keys from JSON-RPC params before the handler's typed
	// params are unmarshalled. Reserved keys (underscore prefix) are
	// platform-injected metadata; handlers see them via dedicated HandlerCtx
	// fields, not as part of their declared params surface.
	reserved, paramsWithoutReserved, paramsErr := extractReservedParams(req.Params)
	if paramsErr != nil {
		writeError(ctx, w, log, req.ID, JSONRPCInvalidParams, paramsErr)
		return
	}

	// Build handler context.
	hctx := &HandlerCtx{
		claims:        &verified.Claims,
		Kind:          verified.Claims.Kind,
		CallerContext: verified.Claims.CallerContext,
		Context:       handlerContext,
		Platform:      platClient,
		Logger: log.With(
			zap.String("tenant_id", verified.Claims.TenantID),
			zap.String("user_id", verified.Claims.UserID),
			zap.String("audit_op", verified.Claims.AuditOpID),
		),
		TenantID:       verified.Claims.TenantID,
		TenantCode:     verified.Claims.TenantCode,
		UserID:         verified.Claims.UserID,
		AgentID:        verified.Claims.AgentID,
		RealUserID:     verified.Claims.RealUserID,
		Roles:          verified.Claims.Roles,
		AuditOp:        verified.Claims.AuditOpID,
		Action:         verified.Claims.Action,
		Permissions:    verified.Claims.Permissions,
		IsSuperadmin:   verified.Claims.IsSuperadmin,
		IsTenantOwner:  verified.Claims.IsTenantOwner,
		IsGlobalUser:   verified.Claims.IsGlobalUser,
		Attributes:     verified.Claims.Attributes,
		RoleAttributes: verified.Claims.RoleAttributes,
		EntityCode:     reserved.EntityCode,
		ObjectIDs:      reserved.ObjectIDs,
		Locale:         reserved.Locale,
	}
	hctx.projectAuthority()

	// Dispatch with params stripped of reserved keys.
	result, err := recoverPanic(func() (any, error) {
		return executeRegisteredHandler(req.Method, hctx, paramsWithoutReserved)
	})
	if err != nil {
		writeHandlerError(ctx, w, hctx.Logger, req.ID, req.Method, err)
		return
	}

	writeJSON(w, http.StatusOK, NewResultResponse(req.ID, result))
}

// handleVerifierDispatch serves a verifier method under the verifier-only token
// audience. Verifier methods ALWAYS require a signed token (no unsigned/test
// bypass), the token authorizes exactly one method, and the Platform client is
// built ONLY from an optional run-as credential header - never the call token.
func handleVerifierDispatch(w http.ResponseWriter, r *http.Request, cfg *Config, log *zap.Logger, req *Request, token string) {
	ctx := r.Context()
	if cfg.JWTSecret == "" {
		writeError(ctx, w, log, req.ID, JSONRPCServerError, errs.New("auth.unauthorized"))
		return
	}
	claims, err := parseVerifierToken(token, cfg.JWTSecret)
	if err != nil {
		writeError(ctx, w, log, req.ID, JSONRPCServerError, errs.New("auth.invalid_token").Because(err))
		return
	}
	// Exact-method binding before registry lookup: the token authorizes exactly
	// one verifier method.
	if claims.Method != req.Method {
		writeError(ctx, w, log, req.ID, JSONRPCServerError, errs.New("auth.invalid_token").
			Because(fmt.Errorf("verifier token method mismatch: the token authorizes %q", claims.Method)))
		return
	}
	ctx = handlerTracePath(tracing.AdoptPath(ctx, r.Header), req.Method)
	r = r.WithContext(ctx)
	log = tracing.Logger(ctx, log)
	fn, ok := lookupVerifier(req.Method)
	if !ok {
		writeError(ctx, w, log, req.ID, JSONRPCMethodNotFound,
			errs.New("handler.not_registered", errs.Args{"method": req.Method}))
		return
	}
	env, err := decodeExternalRequestEnvelope(req.Params)
	if err != nil {
		writeError(ctx, w, log, req.ID, JSONRPCInvalidParams, errs.New("action.invalid_params").Because(err))
		return
	}
	rawBody, err := base64.StdEncoding.DecodeString(env.RawBodyBase64)
	if err != nil {
		writeError(ctx, w, log, req.ID, JSONRPCInvalidParams,
			errs.New("action.invalid_params", errs.Args{"param": "raw_body_base64"}).Because(err))
		return
	}

	// Platform client ONLY from the run-as credential, never the verifier token.
	var platClient *platform.Client
	runAs := r.Header.Get(RunAsTokenHeader)
	if runAs != "" {
		platClient = platform.New(platform.Config{
			BaseURL:    cfg.PlatformURL,
			Token:      runAs,
			HTTPClient: cfg.platformHTTP,
		})
	}

	vctx := &VerifierCtx{
		Context:       r.Context(),
		Logger:        log.With(zap.String("verifier", req.Method), zap.String("action", claims.Action)),
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
		platformHTTP:  cfg.platformHTTP,
	}

	result, err := recoverPanic(func() (any, error) { return fn(vctx) })
	if err != nil {
		writeVerifierError(ctx, w, vctx.Logger, req.ID, err)
		return
	}
	writeJSON(w, http.StatusOK, NewResultResponse(req.ID, result))
}

// writeVerifierError renders a verifier's decline onto the JSON-RPC error
// envelope with its outcome class. An error that is NOT a *VerifierError (a
// verifier bug, a leaked infrastructure error) deliberately renders as
// unavailable rather than as a rejection: Core must not turn an internal fault
// into a permanent 401 that makes the provider drop the delivery.
func writeVerifierError(ctx context.Context, w http.ResponseWriter, log *zap.Logger, id string, err error) {
	var vErr *VerifierError
	if errors.As(err, &vErr) {
		wireErr, rpc := vErr.wire()
		// The reason is logged HERE and nowhere else: it is internal telemetry
		// one hop from a public webhook response.
		log.Info("verifier declined request",
			zap.String("outcome", string(vErr.Outcome)),
			zap.String("reason", vErr.Reason))
		writeError(ctx, w, log, id, rpc, wireErr)
		return
	}
	writeError(ctx, w, log, id, JSONRPCInternalError, errs.New(CodeVerifierUnavailable).Because(err))
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

// writeError is the one way this process answers with a failure. The cause
// attached with Because never crosses the wire, so this is where it is logged:
// once, beside the type the caller received. A failure with no cause is fully
// described by what Core receives and logs, and is not logged twice.
func writeError(ctx context.Context, w http.ResponseWriter, log *zap.Logger, id string, rpcCode int, e *errs.Error) {
	span := trace.SpanFromContext(ctx)
	status := strconv.Itoa(rpcCode)
	span.SetAttributes(attribute.String("rpc.response.status_code", status))
	if errs.Declared(e.Code()) {
		status = e.Code()
	}
	tracing.Fail(span, status)
	if errors.Unwrap(e) != nil {
		level := zap.WarnLevel
		if e.Code() == errs.CodeInternalError {
			// Nothing classified this failure, which makes it a defect.
			level = zap.ErrorLevel
		}
		log.Log(level, "request failed", zap.String("type", e.Code()), zap.Error(e))
	}
	writeJSON(w, http.StatusOK, NewErrorResponse(id, rpcCode, e))
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
func writeHandlerError(ctx context.Context, w http.ResponseWriter, log *zap.Logger, id, method string, err error) {
	if e, ok := errs.From(err); ok {
		if err != error(e) {
			// Wrapped on its way out: the wrapping text is operator context, so
			// the whole chain becomes the cause of a copy the caller never sees.
			wrapped := *e
			e = wrapped.Because(err)
		}
		writeError(ctx, w, log, id, JSONRPCCodeFor(e), e)
		return
	}
	if errors.Is(err, kern.ErrNotFound) {
		writeError(ctx, w, log, id, JSONRPCMethodNotFound, errs.New("handler.not_registered", errs.Args{"method": method}))
		return
	}
	writeError(ctx, w, log, id, JSONRPCInternalError, errs.New(errs.CodeInternalError).Because(err))
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

// withHandlerTimeout bounds the handler by what the platform said it will wait.
// No header leaves the context without a deadline; a header that is not a
// positive whole number is refused, never read as "no bound".
func withHandlerTimeout(parent context.Context, header string) (context.Context, context.CancelFunc, *errs.Error) {
	header = strings.TrimSpace(header)
	if header == "" {
		return parent, func() {}, nil
	}
	ms, err := strconv.ParseInt(header, 10, 64)
	if err != nil || ms <= 0 {
		return nil, nil, errs.New("handler.protocol_mismatch", errs.Args{
			"expected": HandlerTimeoutHeader + ": a positive whole number of milliseconds",
			"got":      header,
		})
	}
	ctx, cancel := context.WithTimeout(parent, time.Duration(ms)*time.Millisecond)
	return ctx, cancel, nil
}

// recoverPanic turns a panicking handler or verifier into an ordinary failure, so the caller
// gets a JSON-RPC error and the stack reaches the request's own logger instead of stderr.
func recoverPanic(run func() (any, error)) (result any, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			result, err = nil, fmt.Errorf("panic: %v\n%s", recovered, debug.Stack())
		}
	}()
	return run()
}
