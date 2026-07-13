package runtime

import (
	"context"

	"go.uber.org/zap"

	"github.com/disciplinedware/declarion-sdk-go/platform"
)

// HandlerCtx is the handler execution context. Provides access to the platform
// client, logger, identity claims, and invocation metadata.
type HandlerCtx struct {
	// Context is the underlying Go context with cancellation/deadline.
	Context context.Context

	// Platform provides typed access to Declarion's data and action APIs.
	// All outbound calls auto-attach the continuation token and trace headers.
	Platform *platform.Client

	// Logger is a structured zap logger pre-tagged with handler,
	// tenant, user, and audit-op IDs. Handlers add their own fields
	// via .With(zap.String(...)) for per-call attribution.
	Logger *zap.Logger

	// Identity holds claims from the continuation token.
	TenantID   string
	TenantCode string
	UserID     string
	AuditOp    string
	Action     string

	// Permissions is the caller's resolved permission list. Sidecar
	// handlers gate fine-grained operations with these.
	Permissions []string

	// Authority dimensions baked into the continuation token at mint.
	// Mirrors the platform's engine.HandlerCtx.
	IsSuperadmin  bool
	IsTenantOwner bool
	IsGlobalUser  bool

	// EntityCode is the entity context the platform invoked this handler with.
	// Populated from the reserved `_entity_code` JSON-RPC param.
	EntityCode string

	// ObjectIDs is the entity-row ids the platform invoked this handler with.
	// Populated from the reserved `_object_ids` JSON-RPC param.
	ObjectIDs []string

	// Baggage is the W3C baggage header value propagated from the platform.
	Baggage string

	// RawBody carries the exact bytes of the HTTP request body, captured
	// before JSON unmarshalling, when the handler was registered with raw-body
	// support. Empty for handlers that did not opt in.
	RawBody []byte

}
