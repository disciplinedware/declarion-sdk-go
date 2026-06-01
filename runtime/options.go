package runtime

import (
	"fmt"
	"time"
)

// Option configures a registration at RegisterFunction time. Options are
// grouped into four conceptual sets that route to either handlerMeta
// (dispatch) or actionMeta (UI/action wrapper):
//
//	A. Handler dispatch:     Timeout, Async, Retry, Idempotent, Invoke,
//	                         AllowNoObjects, ReadOnly, SuppressEvents, Audit
//	B. Handler auth/security: Unauthenticated, RawBodyAccess, MaxBodyBytes,
//	                          RequestVerifier, RequestDedupKeyExpr,
//	                          RequestDedupKeyParam, TenantFromHeader,
//	                          TenantFromPayloadLookup
//	C. Action UI/metadata:   NameEN, NameRU, Icon, Destructive, LongRunning,
//	                         ProgressScreen
//	D. Action gate control:  Action, NoAction, RequiredPermission
//
// Any group-C option implicitly initializes actionMeta, causing the function
// to be emitted as an action: entry in the generated YAML. Group-D's Action()
// forces an action wrapper without UI metadata; NoAction() suppresses it
// (panics when combined with a group-C option — contradictory intent).
type Option interface {
	apply(*registration)
}

// optionFn adapts a plain function into an Option. Internal helper.
type optionFn func(*registration)

func (f optionFn) apply(r *registration) { f(r) }

// ensureActionMeta initializes r.actionMeta on first group-C/D access. Pulled
// out as a helper so every option constructor goes through one chokepoint.
func ensureActionMeta(r *registration) {
	if r.actionMeta == nil {
		r.actionMeta = &ActionMetadata{}
	}
}

// validBackoffs is the closed set of accepted backoff values.
var validBackoffs = map[string]bool{
	"exponential": true,
	"linear":      true,
	"constant":    true,
}

// validInvokes is the closed set of accepted invoke modes for handler dispatch.
var validInvokes = map[string]bool{
	"each":    true,
	"unbound": true,
	"batch":   true,
}

// --- Group A: handler dispatch ---

// Timeout sets the per-call deadline for the handler. Emitted in generated
// YAML as the "timeout" field. Does not enforce a deadline at the Go call
// layer; the platform enforces it server-side via the timeout config.
func Timeout(d time.Duration) Option {
	return optionFn(func(r *registration) { r.handlerMeta.Timeout = &d })
}

// Async marks the handler as async (enqueued through declarion.jobs).
// Emitted as `async: true`.
func Async() Option {
	return optionFn(func(r *registration) { r.handlerMeta.IsAsync = true })
}

// Retry attaches a retry policy. maxAttempts must be >= 2. backoff must be
// one of "exponential", "linear", "constant"; panics otherwise.
func Retry(maxAttempts int, backoff string) Option {
	if maxAttempts < 2 {
		panic(fmt.Sprintf("runtime.Retry: maxAttempts must be >= 2, got %d", maxAttempts))
	}
	if !validBackoffs[backoff] {
		panic(fmt.Sprintf("runtime.Retry: backoff must be one of exponential|linear|constant, got %q", backoff))
	}
	return optionFn(func(r *registration) {
		r.handlerMeta.Retry = &RetryConfig{MaxAttempts: maxAttempts, Backoff: backoff}
	})
}

// Idempotent marks the handler as safe to retry/reinvoke without duplicate
// side effects. Emitted as `idempotent: true`.
func Idempotent() Option {
	return optionFn(func(r *registration) { r.handlerMeta.Idempotent = true })
}

// Invoke sets the handler invoke mode. Must be one of "each" (default),
// "unbound", "batch". Panics on unknown value. Emitted as `invoke: <mode>`.
func Invoke(mode string) Option {
	if !validInvokes[mode] {
		panic(fmt.Sprintf("runtime.Invoke: mode must be one of each|unbound|batch, got %q", mode))
	}
	return optionFn(func(r *registration) { r.handlerMeta.Invoke = mode })
}

// AllowNoObjects permits the handler to run with zero _object_ids (typically
// paired with Invoke("unbound") for tenant-scoped actions).
// Emitted as `allow_no_objects: true`.
func AllowNoObjects() Option {
	return optionFn(func(r *registration) { r.handlerMeta.AllowNoObjects = true })
}

// ReadOnly marks the handler as side-effect-free; composites and replay paths
// may skip durability writes. Emitted as `read_only: true`.
func ReadOnly() Option {
	return optionFn(func(r *registration) { r.handlerMeta.ReadOnly = true })
}

// SuppressEvents disables implicit success-event emission for this handler.
// Emitted as `suppress_events: true`.
func SuppressEvents() Option {
	return optionFn(func(r *registration) { r.handlerMeta.SuppressEvents = true })
}

// Audit overrides the action-default audit setting for this handler.
// `Audit(true)` forces an audit row on each invocation; `Audit(false)` opts
// out. Without Audit(...) the platform applies the action's own default.
// Emitted as `audit: true|false`.
func Audit(enabled bool) Option {
	return optionFn(func(r *registration) { r.handlerMeta.Audit = &enabled })
}

// --- Group B: auth/security ---

// Unauthenticated exposes the handler at /api/actions/{code} without a JWT.
// Required for public inbound webhooks (Telegram, Stripe, etc.). SHOULD be
// paired with MaxBodyBytes() and RequestDedupKey() — declarion-core's
// validator rejects unauthenticated+dedup-or-verifier handlers without
// TenantFrom. Emitted as `unauthenticated: true`.
func Unauthenticated() Option {
	return optionFn(func(r *registration) { r.handlerMeta.IsUnauthenticated = true })
}

// RawBodyAccess exposes the exact HTTP request bytes on Ctx.RawBody. Required
// for HMAC verification of webhook payloads. Forbidden with Async() — raw
// bytes are not persisted across job re-execution. Emitted as
// `raw_body_access: true`.
func RawBodyAccess() Option {
	return optionFn(func(r *registration) { r.handlerMeta.HasRawBodyAccess = true })
}

// MaxBodyBytes caps the inbound HTTP request body size, in bytes.
// Zero leaves the platform default in effect. Use a conservative value for
// unauthenticated webhook handlers. Emitted as `max_body_bytes: <n>`.
func MaxBodyBytes(n int64) Option {
	if n < 0 {
		panic(fmt.Sprintf("runtime.MaxBodyBytes: must be non-negative, got %d", n))
	}
	return optionFn(func(r *registration) { r.handlerMeta.MaxBodyBytes = n })
}

// RequestVerifier names a verifier plugin (e.g. "telegram", "stripe") that
// runs before the handler to validate the raw request payload. Emitted as
// `request_verifier: <name>`.
func RequestVerifier(name string) Option {
	if name == "" {
		panic("runtime.RequestVerifier: name must be non-empty")
	}
	return optionFn(func(r *registration) { r.handlerMeta.RequestVerifier = name })
}

// RequestDedupKeyExpr extracts the deduplication key from the payload via an
// expression evaluated against $payload. Use for webhooks that carry a stable
// per-event id at the JSON top level (Telegram's `update_id`, Stripe's `id`).
func RequestDedupKeyExpr(expr string) Option {
	if expr == "" {
		panic("runtime.RequestDedupKeyExpr: expression must be non-empty")
	}
	return optionFn(func(r *registration) {
		r.handlerMeta.RequestDedupKey = &RequestDedupKeyConfig{Source: "expr", Expression: expr}
	})
}

// RequestDedupKeyParam reads the deduplication key from a named action param.
func RequestDedupKeyParam(name string) Option {
	if name == "" {
		panic("runtime.RequestDedupKeyParam: name must be non-empty")
	}
	return optionFn(func(r *registration) {
		r.handlerMeta.RequestDedupKey = &RequestDedupKeyConfig{Source: "param", ParamName: name}
	})
}

// TenantFromHeader declares that the tenant id/code is carried in a named
// header, trusted by the webhook verifier upstream. Use only when the header
// is set by a trusted intermediary (e.g. our own verifier), never when it
// comes from the caller directly.
func TenantFromHeader(name string) Option {
	if name == "" {
		panic("runtime.TenantFromHeader: header name must be non-empty")
	}
	return optionFn(func(r *registration) {
		r.handlerMeta.TenantFrom = &TenantFromConfig{Source: "header", HeaderName: name}
	})
}

// TenantFromPayloadLookup declares that the sidecar handler resolves the
// tenant at dispatch time from the payload (e.g. lookup by bot_id in a
// community_bot row). No pre-dispatch trust is assumed; the sidecar is the
// authority.
func TenantFromPayloadLookup() Option {
	return optionFn(func(r *registration) {
		r.handlerMeta.TenantFrom = &TenantFromConfig{Source: "payload_lookup"}
	})
}

// --- Group C: action UI/metadata (implies action wrapper) ---

// NameEN sets the English display name on the action wrapper. Initializes
// actionMeta on first call. Emitted only under the actions: block — the YAML
// emitter mirrors it into the handler-block display.name when both names are
// needed by declarion. Single source of truth at registration.
func NameEN(s string) Option {
	return optionFn(func(r *registration) {
		ensureActionMeta(r)
		r.actionMeta.Display.NameEN = s
	})
}

// NameRU sets the Russian display name on the action wrapper. Initializes
// actionMeta on first call.
func NameRU(s string) Option {
	return optionFn(func(r *registration) {
		ensureActionMeta(r)
		r.actionMeta.Display.NameRU = s
	})
}

// Icon sets the action's icon name (e.g. "refresh", "trash", "columns-3").
// Initializes actionMeta on first call.
func Icon(s string) Option {
	return optionFn(func(r *registration) {
		ensureActionMeta(r)
		r.actionMeta.Display.Icon = s
	})
}

// Destructive marks the action as irreversible. The UI shows a confirmation
// dialog before invoking. Initializes actionMeta on first call. Emitted as
// `destructive: true` under the action entry.
func Destructive() Option {
	return optionFn(func(r *registration) {
		ensureActionMeta(r)
		r.actionMeta.Destructive = true
	})
}

// LongRunning indicates the action takes long enough that the UI should show
// a progress affordance instead of an inline spinner. Initializes actionMeta
// on first call. Emitted as `long_running: true`.
func LongRunning() Option {
	return optionFn(func(r *registration) {
		ensureActionMeta(r)
		r.actionMeta.LongRunning = true
	})
}

// ProgressScreen names the screen/route the UI should navigate to while a
// long-running action runs (e.g. "replay_jobs_list"). Initializes actionMeta
// on first call. Emitted as `progress_screen: <name>`.
func ProgressScreen(s string) Option {
	if s == "" {
		panic("runtime.ProgressScreen: screen name must be non-empty")
	}
	return optionFn(func(r *registration) {
		ensureActionMeta(r)
		r.actionMeta.ProgressScreen = s
	})
}

// --- Group D: action gate control ---

// Action forces the function to be emitted as an action: entry even when no
// group-C UI option is supplied. Use when a function needs an action wrapper
// (for permission gating, UI exposure, etc.) but has no localized display
// fields — declarion will derive a default name from the method code.
func Action() Option {
	return optionFn(func(r *registration) { r.forceAction = true })
}

// NoAction suppresses the action: entry even if group-C options would
// otherwise initialize actionMeta. Panics at registration if combined with
// any group-C/D action-positive option (Action, NameEN/RU, Icon, Destructive,
// LongRunning, ProgressScreen, RequiredPermission) — that combination is a
// programming error, not a silent drop.
//
// Used by swiftward UDFs and other handler-only functions that are pure
// compute (no permission gate, no UI exposure).
func NoAction() Option {
	return optionFn(func(r *registration) { r.noAction = true })
}

// RequiredPermission overrides the default permission code derived from the
// method name (used for actions that share a permission with a sibling
// action). Initializes actionMeta on first call. Emitted as
// `required_permission: <code>` under the action entry.
func RequiredPermission(code string) Option {
	if code == "" {
		panic("runtime.RequiredPermission: code must be non-empty")
	}
	return optionFn(func(r *registration) {
		ensureActionMeta(r)
		r.actionMeta.RequiredPermission = code
	})
}
