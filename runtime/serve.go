package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/disciplinedware/declarion-sdk-go/platform"
)

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

	// Logger overrides the default structured logger.
	Logger *slog.Logger

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
		c.Logger = slog.Default()
	}
	if c.ShutdownTimeout == 0 {
		c.ShutdownTimeout = 10 * time.Second
	}
}

// Serve starts the JSON-RPC sidecar server with the given handlers.
// Blocks until SIGTERM/SIGINT, then gracefully shuts down.
func Serve(cfg Config, handlers ...HandlerRegistration) error {
	cfg.withDefaults()

	// Startup warnings for misconfiguration.
	if cfg.JWTSecret == "" {
		cfg.Logger.Warn("DECLARION_JWT_SECRET not set: continuation tokens will NOT be signature-verified (trusting network boundary)")
	}
	if cfg.PlatformURL == "" {
		cfg.Logger.Warn("DECLARION_PLATFORM_URL not set: ctx.Platform calls will fail")
	}

	registry := make(map[string]HandlerRegistration, len(handlers))
	for _, h := range handlers {
		if _, exists := registry[h.Method]; exists {
			return fmt.Errorf("duplicate handler method: %s", h.Method)
		}
		registry[h.Method] = h
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /rpc", func(w http.ResponseWriter, r *http.Request) {
		handleRPC(w, r, registry, &cfg)
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
		"addr", cfg.Addr,
		"handlers", len(registry),
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
		cfg.Logger.Info("received signal, shutting down", "signal", sig)
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

func handleRPC(w http.ResponseWriter, r *http.Request, registry map[string]HandlerRegistration, cfg *Config) {
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
				fmt.Sprintf("request body exceeded %d bytes (limit %d)", maxErr.Limit, MaxRequestSize),
				"", false))
			return
		}
		writeJSON(w, http.StatusOK, NewErrorResponse("", JSONRPCParseError, "read error", "", false))
		return
	}

	var req Request
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusOK, NewErrorResponse("", JSONRPCParseError, "invalid JSON", "", false))
		return
	}

	// Check protocol version (now req.ID is available for error correlation).
	protoVer := r.Header.Get("X-Declarion-Protocol-Version")
	if protoVer != "" && protoVer != ProtocolVersion {
		writeJSON(w, http.StatusOK, NewErrorResponse(req.ID, JSONRPCAppError,
			fmt.Sprintf("protocol version mismatch: expected %s, got %s", ProtocolVersion, protoVer),
			CodeProtocolMismatch, false))
		return
	}

	if req.JSONRPC != "2.0" {
		writeJSON(w, http.StatusOK, NewErrorResponse(req.ID, JSONRPCInvalidRequest, "jsonrpc must be 2.0", "", false))
		return
	}

	// Find handler.
	handler, ok := registry[req.Method]
	if !ok {
		writeJSON(w, http.StatusOK, NewErrorResponse(req.ID, JSONRPCMethodNotFound,
			fmt.Sprintf("method %q not found", req.Method), "", false))
		return
	}

	// Extract continuation token from Authorization header.
	token := extractBearer(r.Header.Get("Authorization"))
	traceparent := r.Header.Get("traceparent")
	baggage := r.Header.Get("baggage")

	// Enforce RequireToken: reject requests without a valid bearer token.
	if cfg.RequireToken && token == "" {
		writeJSON(w, http.StatusOK, NewErrorResponse(req.ID, JSONRPCAppError,
			"authorization required", CodePermissionDenied, false))
		return
	}

	// Parse claims from token (identity extraction).
	var tenantID, tenantCode, userID, auditOp, action string
	if token != "" {
		claims, err := parseHandlerToken(token, cfg.JWTSecret)
		if err != nil {
			cfg.Logger.Warn("invalid continuation token", "error", err, "method", req.Method)
			writeJSON(w, http.StatusOK, NewErrorResponse(req.ID, JSONRPCAppError,
				"invalid continuation token", CodePermissionDenied, false))
			return
		}
		tenantID = claims.TenantID
		tenantCode = claims.TenantCode
		userID = claims.UserID
		auditOp = claims.AuditOpID
		action = claims.Action
	}

	// Build platform client.
	platClient := platform.New(platform.Config{
		BaseURL:     cfg.PlatformURL,
		Token:       token,
		Traceparent: traceparent,
		Baggage:     baggage,
	})

	// Extract reserved `_object_ids` from JSON-RPC params before the handler's
	// typed params are unmarshalled. Reserved keys (underscore prefix) are
	// platform-injected metadata; handlers see them via dedicated Ctx fields,
	// not as part of their declared params surface.
	objectIDs, paramsWithoutReserved, err := extractObjectIDs(req.Params)
	if err != nil {
		writeJSON(w, http.StatusOK, NewErrorResponse(req.ID, JSONRPCInvalidParams,
			fmt.Sprintf("invalid _object_ids: %s", err), CodeValidation, false))
		return
	}

	// Build handler context.
	hctx := &Ctx{
		Context:  r.Context(),
		Platform: platClient,
		Logger: cfg.Logger.With(
			"method", req.Method,
			"tenant_id", tenantID,
			"user_id", userID,
			"audit_op", auditOp,
		),
		TenantID:   tenantID,
		TenantCode: tenantCode,
		UserID:     userID,
		AuditOp:    auditOp,
		Action:     action,
		ObjectIDs:  objectIDs,
		Baggage:    baggage,
	}

	// Dispatch with params stripped of reserved keys.
	result, err := handler.Dispatch(hctx, paramsWithoutReserved)
	if err != nil {
		var appErr *AppError
		if errors.As(err, &appErr) {
			writeJSON(w, http.StatusOK, NewErrorResponse(req.ID, appErr.Code,
				appErr.Message, appErr.DeclarionCode, appErr.Retryable))
		} else {
			cfg.Logger.Error("handler error", "method", req.Method, "error", err)
			writeJSON(w, http.StatusOK, NewErrorResponse(req.ID, JSONRPCInternalError,
				err.Error(), CodeInternal, false))
		}
		return
	}

	writeJSON(w, http.StatusOK, NewResultResponse(req.ID, result))
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

// extractObjectIDs pulls the reserved `_object_ids` field from JSON-RPC params
// and returns it plus the params with that key removed. Returns (nil, raw, nil)
// when the field is absent (handler called without entity ids — fine for
// invoke=unbound). Errors only on type mismatch.
func extractObjectIDs(raw json.RawMessage) ([]string, json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, raw, nil
	}
	var bag map[string]json.RawMessage
	if err := json.Unmarshal(raw, &bag); err != nil {
		// Not an object — pass through untouched (handler with positional/array
		// params, or empty body). No object_ids to extract.
		return nil, raw, nil
	}
	idsRaw, ok := bag["_object_ids"]
	if !ok {
		return nil, raw, nil
	}
	delete(bag, "_object_ids")
	var ids []string
	if err := json.Unmarshal(idsRaw, &ids); err != nil {
		return nil, raw, fmt.Errorf("expected string array: %w", err)
	}
	cleaned, err := json.Marshal(bag)
	if err != nil {
		return nil, raw, fmt.Errorf("re-marshal params: %w", err)
	}
	return ids, cleaned, nil
}
