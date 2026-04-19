package runtime

import (
	"context"
	"log/slog"

	"github.com/disciplinedware/declarion-sdk-go/platform"
)

// Ctx is the handler execution context. Provides access to the platform client,
// logger, and identity claims extracted from the continuation token.
type Ctx struct {
	// Context is the underlying Go context with cancellation/deadline.
	Context context.Context

	// Platform provides typed access to Declarion's data and action APIs.
	// All outbound calls auto-attach the continuation token and trace headers.
	Platform *platform.Client

	// Logger is a structured logger pre-tagged with handler, tenant, user, and trace IDs.
	Logger *slog.Logger

	// Identity holds claims from the continuation token.
	TenantID   string
	TenantCode string
	UserID     string
	AuditOp    string
	Action     string

	// ObjectIDs is the entity-row ids the platform invoked this handler with.
	// Populated from the reserved `_object_ids` field in the JSON-RPC params
	// envelope (extracted by the SDK before the handler's typed params are
	// unmarshalled, so handlers see only their declared params plus this
	// dedicated field).
	//
	// Length:
	//   invoke: array (handler default)  → 0..N (whatever caller passed)
	//   invoke: each                     → exactly 1 (platform fans out)
	//   invoke: unbound                  → 0 (handler ignores ids)
	ObjectIDs []string

	// Baggage is the W3C baggage header value propagated from the platform.
	Baggage string
}
