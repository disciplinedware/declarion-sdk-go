package runtime

import (
	"fmt"
	"time"
)

// HandlerOption configures a HandlerRegistration at registration time.
// The metadata is stored on the registration and consumed by the YAML generator;
// Serve does not read these fields.
type HandlerOption func(*HandlerMetadata)

// HandlerMetadata carries optional per-handler metadata attached at registration
// time. Populated by HandlerOption functions; read by GenerateHandlersYAML.
type HandlerMetadata struct {
	Timeout *time.Duration
	NameEN  string
	NameRU  string
	Retry   *RetryConfig
	IsAsync bool

	// Webhook-action flags. Set when the handler is an inbound webhook
	// reachable at /api/actions/{code} without authentication. Each flag
	// has a YAML-side counterpart in declarion-core engine.Handler.

	// IsUnauthenticated, when true, exposes the handler at /api/actions/{code}
	// without requiring a session/JWT. Required for public webhook endpoints
	// (Telegram, Stripe, etc.). Must be paired with dedup + tenant resolution
	// — see declarion-core engine validator for the safety rules.
	IsUnauthenticated bool

	// HasRawBodyAccess, when true, exposes the raw request bytes on Ctx.RawBody.
	// Required for HMAC verification of webhooks. Forbids async dispatch (raw
	// bytes are not persisted across job re-execution).
	HasRawBodyAccess bool

	// MaxBodyBytes caps the inbound HTTP request body size, in bytes.
	// Zero means "use the platform default". Unauthenticated handlers SHOULD
	// always set an explicit cap.
	MaxBodyBytes int64

	// RequestDedupKey declares how the platform extracts a deduplication key
	// from the request to short-circuit duplicate webhook deliveries.
	RequestDedupKey *RequestDedupKeyConfig

	// TenantFrom declares trusted tenant resolution for unauthenticated webhook
	// handlers (since there's no JWT to read tenant from).
	TenantFrom *TenantFromConfig
}

// RequestDedupKeyConfig mirrors declarion-core engine.RequestDedupKeyConfig
// so SDK-generated YAML deserializes losslessly back into the core.
type RequestDedupKeyConfig struct {
	// Source: "param", "expr", or "header".
	Source string
	// ParamName: when Source == "param", names the action param holding the key.
	ParamName string
	// Expression: when Source == "expr", expression evaluated against $payload.
	// Example: "$payload.update_id" for Telegram webhooks.
	Expression string
	// RequiredForMutating: when true, mutating verbs without a key are rejected.
	RequiredForMutating bool
}

// TenantFromConfig mirrors declarion-core engine.TenantFromConfig.
type TenantFromConfig struct {
	// Source: "header", "verifier_context", or "payload_lookup".
	Source string
	// HeaderName: when Source == "header", the HTTP header carrying tenant id/code.
	HeaderName string
}

// RetryConfig configures automatic retry behaviour for a handler.
type RetryConfig struct {
	// MaxAttempts is the total number of attempts (including the first).
	// Must be >= 2.
	MaxAttempts int
	// Backoff is the retry delay strategy. One of "exponential", "linear", "constant".
	Backoff string
}

// validBackoffs is the closed set of accepted backoff values.
var validBackoffs = map[string]bool{
	"exponential": true,
	"linear":      true,
	"constant":    true,
}

// Timeout sets the per-call deadline for the handler.
// Stored on the registration; emitted in generated YAML as the "timeout" field.
// Does not enforce a deadline at the Go call layer.
func Timeout(d time.Duration) HandlerOption {
	return func(m *HandlerMetadata) { m.Timeout = &d }
}

// NameEN sets the English display name. Emitted in generated YAML under
// display.name.en.
func NameEN(s string) HandlerOption {
	return func(m *HandlerMetadata) { m.NameEN = s }
}

// NameRU sets the Russian display name. Emitted in generated YAML under
// display.name.ru when non-empty.
func NameRU(s string) HandlerOption {
	return func(m *HandlerMetadata) { m.NameRU = s }
}

// Retry attaches a retry policy to the handler.
// maxAttempts must be >= 2.
// backoff must be one of "exponential", "linear", "constant"; panics otherwise.
func Retry(maxAttempts int, backoff string) HandlerOption {
	if maxAttempts < 2 {
		panic(fmt.Sprintf("runtime.Retry: maxAttempts must be >= 2, got %d", maxAttempts))
	}
	if !validBackoffs[backoff] {
		panic(fmt.Sprintf("runtime.Retry: backoff must be one of exponential|linear|constant, got %q", backoff))
	}
	return func(m *HandlerMetadata) {
		m.Retry = &RetryConfig{MaxAttempts: maxAttempts, Backoff: backoff}
	}
}

// Async marks the handler as async. Stored on the registration; emitted in
// generated YAML as async: true.
func Async() HandlerOption {
	return func(m *HandlerMetadata) { m.IsAsync = true }
}

// Unauthenticated exposes the handler at /api/actions/{code} without a JWT.
// Required for public inbound webhooks (Telegram, Stripe, etc.). SHOULD be
// paired with MaxBodyBytes() and RequestDedupKey() — declarion-core's
// validator rejects unauthenticated+dedup-or-verifier handlers without
// TenantFrom.
//
// Emitted in generated YAML as `unauthenticated: true`.
func Unauthenticated() HandlerOption {
	return func(m *HandlerMetadata) { m.IsUnauthenticated = true }
}

// RawBodyAccess exposes the exact HTTP request bytes on Ctx.RawBody. Required
// for HMAC verification of webhook payloads (e.g. checking the Telegram
// X-Telegram-Bot-Api-Secret-Token header against the per-bot secret, or
// verifying a Stripe-Signature header over the raw body).
//
// Forbidden with Async() — raw bytes are not persisted across job re-execution.
//
// Emitted as `raw_body_access: true`.
func RawBodyAccess() HandlerOption {
	return func(m *HandlerMetadata) { m.HasRawBodyAccess = true }
}

// MaxBodyBytes caps the inbound HTTP request body size, in bytes. Use a
// conservative value for unauthenticated webhook handlers to limit DoS
// exposure. Zero leaves the platform default in effect.
//
// Emitted as `max_body_bytes: <n>`.
func MaxBodyBytes(n int64) HandlerOption {
	if n < 0 {
		panic(fmt.Sprintf("runtime.MaxBodyBytes: must be non-negative, got %d", n))
	}
	return func(m *HandlerMetadata) { m.MaxBodyBytes = n }
}

// RequestDedupKeyExpr extracts the deduplication key from the payload via an
// expression evaluated against $payload. Use for webhooks that carry a stable
// per-event id at the JSON top level (Telegram's `update_id`, Stripe's `id`).
//
// Emitted as:
//
//	request_dedup_key:
//	  source: expr
//	  expression: <expr>
func RequestDedupKeyExpr(expr string) HandlerOption {
	if expr == "" {
		panic("runtime.RequestDedupKeyExpr: expression must be non-empty")
	}
	return func(m *HandlerMetadata) {
		m.RequestDedupKey = &RequestDedupKeyConfig{Source: "expr", Expression: expr}
	}
}

// RequestDedupKeyParam reads the deduplication key from a named action param.
//
// Emitted as:
//
//	request_dedup_key:
//	  source: param
//	  param_name: <name>
func RequestDedupKeyParam(name string) HandlerOption {
	if name == "" {
		panic("runtime.RequestDedupKeyParam: name must be non-empty")
	}
	return func(m *HandlerMetadata) {
		m.RequestDedupKey = &RequestDedupKeyConfig{Source: "param", ParamName: name}
	}
}

// TenantFromHeader declares that the tenant id/code is carried in a named
// header, trusted by the webhook verifier upstream. Use only when the
// header is set by a trusted intermediary (e.g. our own verifier), never
// when the header comes from the caller directly.
//
// Emitted as:
//
//	tenant_from:
//	  source: header
//	  header_name: <name>
func TenantFromHeader(name string) HandlerOption {
	if name == "" {
		panic("runtime.TenantFromHeader: header name must be non-empty")
	}
	return func(m *HandlerMetadata) {
		m.TenantFrom = &TenantFromConfig{Source: "header", HeaderName: name}
	}
}

// TenantFromPayloadLookup declares that the sidecar handler resolves the
// tenant at dispatch time from the payload (e.g. lookup by bot_id in a
// community_bot row). No pre-dispatch trust is assumed; the sidecar is the
// authority.
//
// Emitted as:
//
//	tenant_from:
//	  source: payload_lookup
func TenantFromPayloadLookup() HandlerOption {
	return func(m *HandlerMetadata) {
		m.TenantFrom = &TenantFromConfig{Source: "payload_lookup"}
	}
}
