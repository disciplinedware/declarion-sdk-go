package runtime

// VerifierResult is the strict success result an application verifier returns.
// It carries only facts Declarion may consume - never authority. Declarion Core
// validates it against the action's declared principal/idempotency modes and
// bounds before dispatch; a returned authority field is impossible by
// construction here (no such field exists). `context` is the sole application
// extension point and is delivered read-only to the handler.
//
// Mirrors declarion-core internal/engine.VerifierResult; the two MUST stay
// wire-compatible.
//
// Single-user model: a verifier ALWAYS yields (tenant, user). The handler runs
// as that real user with its live grants under the normal L1 gate. There is no
// principal mode, no integration id, and no per-action permission list.
type VerifierResult struct {
	// TargetTenantID is the canonical tenant UUID the handler dispatches into.
	// OPTIONAL: leave it empty and Core uses the verifier declaration's
	// `dispatch_tenant`. Fill it only when the verifier is what determines the
	// tenant (e.g. from the authenticated bot's active registration). A tenant
	// CODE such as `_global` is rejected here - Core resolves codes itself.
	TargetTenantID string `json:"target_tenant_id,omitempty"`

	// UserID is the UUID of the user the handler runs as. OPTIONAL: leave it
	// empty and Core uses the verifier declaration's `dispatch_user` (a
	// `_global` service user named in YAML, so "who does this webhook run as"
	// is auditable from the schema). It never carries permissions - Core
	// reloads the user's live authority in the target tenant.
	UserID string `json:"user_id,omitempty"`

	// Params are optional trusted values Core merges into the handler's input
	// params by name (verifier > path > body precedence; a conflicting body
	// value is a 400). This is how the verifier hands routing facts (e.g. the
	// authenticated bot) to the handler while the handler stays verifier-agnostic
	// - it just receives params. Bounded; data only, never authority.
	Params map[string]any `json:"params,omitempty"`
}
