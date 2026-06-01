package runtime

import "time"

// HandlerMetadata carries optional per-handler dispatch metadata attached at
// registration time. Populated by Option functions via RegisterFunction; read
// by GenerateFunctionsYAML when emitting the handlers: block.
//
// All fields are dispatch-side concerns (timeout, async, retry, webhook flags,
// rate limiting). UI/action-wrapper metadata lives on ActionMetadata.
type HandlerMetadata struct {
	// Timeout sets a per-call deadline. Emitted as "timeout: 30s" etc.
	Timeout *time.Duration

	// Retry attaches a retry policy. Emitted as a retry: block.
	Retry *RetryConfig

	// IsAsync marks the handler as async (enqueued through declarion.jobs).
	IsAsync bool

	// Idempotent marks the handler as safe to retry/reinvoke without
	// duplicate side-effects.
	Idempotent bool

	// Invoke is one of "each" (default), "unbound", "batch" — controls how
	// declarion invokes the handler against the action's object set.
	Invoke string

	// AllowNoObjects, when true, permits the handler to run with zero
	// _object_ids (paired with invoke=unbound for tenant-scoped actions).
	AllowNoObjects bool

	// ReadOnly marks the handler as side-effect-free. Used by composites
	// and replay paths to skip durability writes.
	ReadOnly bool

	// SuppressEvents disables the implicit event emission on success.
	SuppressEvents bool

	// Audit (when non-nil) overrides the action-default audit setting for
	// this handler. true → always audit; false → never audit.
	Audit *bool

	// Webhook-action flags — set when the handler is an inbound webhook
	// reachable at /api/actions/{code} without authentication.

	// IsUnauthenticated exposes the handler at /api/actions/{code} without
	// a session/JWT. Required for public webhook endpoints (Telegram,
	// Stripe, etc.). Must be paired with dedup + tenant resolution.
	IsUnauthenticated bool

	// HasRawBodyAccess exposes the raw request bytes on Ctx.RawBody.
	// Required for HMAC verification of webhooks. Forbids async dispatch
	// (raw bytes are not persisted across job re-execution).
	HasRawBodyAccess bool

	// MaxBodyBytes caps the inbound HTTP request body size, in bytes.
	// Zero means "use the platform default". Unauthenticated handlers
	// SHOULD always set an explicit cap.
	MaxBodyBytes int64

	// RequestVerifier names a verifier plugin (e.g. "telegram", "stripe")
	// that runs before the handler to validate the raw request payload.
	RequestVerifier string

	// RequestDedupKey declares how the platform extracts a deduplication
	// key from the request to short-circuit duplicate webhook deliveries.
	RequestDedupKey *RequestDedupKeyConfig

	// TenantFrom declares trusted tenant resolution for unauthenticated
	// webhook handlers (since there's no JWT to read tenant from).
	TenantFrom *TenantFromConfig
}

// ActionMetadata carries optional per-action wrapper metadata attached at
// registration time. Populated by Option functions via RegisterFunction; read
// by GenerateFunctionsYAML when emitting the actions: block.
//
// A registration with nil ActionMetadata emits no action: entry (handler-only,
// e.g. pure-compute UDFs that do not need a permission gate or UI exposure).
// Group-C options (NameEN, NameRU, Icon, Destructive, LongRunning,
// ProgressScreen) and group-D Action() / RequiredPermission() initialize
// this struct.
type ActionMetadata struct {
	// Handler is always equal to registration.method; the emitter fills it.
	Handler string

	// Display holds the user-facing name + icon for the action button.
	Display Display

	// Destructive marks the action as irreversible (UI shows a confirm).
	Destructive bool

	// LongRunning indicates the action takes long enough that the UI
	// should show a progress affordance instead of an inline spinner.
	LongRunning bool

	// ProgressScreen names the screen/route to navigate the user to while
	// a long-running action runs (e.g. "replay_jobs_list").
	ProgressScreen string

	// RequiredPermission overrides the default permission code derived
	// from the method name (used for actions that share a permission with
	// a sibling action).
	RequiredPermission string
}

// Display is the localized name + icon block emitted under actions:<code>:display.
type Display struct {
	NameEN string
	NameRU string
	Icon   string
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
