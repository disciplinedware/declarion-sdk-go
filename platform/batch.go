package platform

import (
	"github.com/disciplinedware/declarion-sdk-go/errs"

	"context"
	"encoding/json"
	"fmt"
)

// BatchOp is one action call inside a system.batch invocation. Mirrors the
// platform handler's wire shape: {action, object_ids?, params}. Sync vs async
// is a property of the target action's registration, not of the op.
//
// ObjectIDs is the op's PK channel - the rows a write op addresses by primary
// key (__update / __delete / __restore, and entity-scoped actions that select
// existing rows). The system.batch dispatcher reads it from the op's TOP LEVEL
// (json "object_ids"), NOT from params, into the sub-handler's ctx.ObjectIDs
// (see declarion-core handler/batch_handler.go: BatchOp.ObjectIDs ->
// ExecRequest.ObjectIDs). Putting object_ids inside Params leaves ctx.ObjectIDs
// empty and the sub-handler fails with "requires object_ids". Empty for
// __create / __upsert (which produce PKs or match by unique_by) and for
// handlers that take no objects.
//
// Params is the FLAT action body for the target endpoint - the non-PK
// top-level keys a direct POST /api/actions/{action} would carry (entity,
// fields, condition, ...). The dispatcher applies them verbatim. No `_ids`,
// no `items[]`, and never `object_ids` (that lives at the op top level).
type BatchOp struct {
	Action    string         `json:"action"`
	ObjectIDs []string       `json:"object_ids,omitempty"`
	Params    map[string]any `json:"params"`
}

// BatchOpResult is the per-op result returned by system.batch.
type BatchOpResult struct {
	Index int `json:"index"`
	// Action names the dispatched op, so an entry says WHICH op it describes
	// without matching its index against the request by position.
	Action string `json:"action,omitempty"`
	OK     bool   `json:"ok"`
	Result any    `json:"result,omitempty"`
	// The failure, in the one shape every carrier sends.
	Error *errs.Error `json:"error,omitempty"`
}

// BatchResponse is the wire-level result of system.batch. Committed=false
// means at least one op failed and the whole transaction rolled back; the
// failing op's index and error are in Results.
type BatchResponse struct {
	Results   []BatchOpResult `json:"results"`
	Committed bool            `json:"committed"`
}

// Batch is a fluent builder for the system.batch action. Use NewBatch to
// open one, .Call / .Create / .Upsert / .Update / .Delete / .Restore to add
// ops, and .Execute to send.
//
// The batch is sync + atomic by definition: Execute returns once the
// platform has committed (or rolled back) the entire transaction.
//
// Per-row variation across the 5 data.* actions is expressed here: one op
// per row, one FLAT envelope per op. The system.batch dispatcher applies
// the same permission/audit/lifecycle gate as a direct action call, so the
// only thing the SDK has to do is build the right ops.
type Batch struct {
	c          *Client
	ops        []BatchOp
	tenantID   string
	tenantCode string
}

// NewBatch starts a new system.batch builder. The returned builder is not
// safe for concurrent use; share results, not the builder.
func (c *Client) NewBatch() *Batch {
	return &Batch{c: c}
}

// WithTargetTenantID sends X-Declarion-Tenant-ID for this batch execution.
// Mutually exclusive with WithTargetTenantCode; the last call wins.
func (b *Batch) WithTargetTenantID(tenantID string) *Batch {
	b.tenantID = tenantID
	b.tenantCode = ""
	return b
}

// WithTargetTenantCode sends X-Declarion-Tenant-Code for this batch execution.
// Mutually exclusive with WithTargetTenantID; the last call wins.
func (b *Batch) WithTargetTenantCode(tenantCode string) *Batch {
	b.tenantCode = tenantCode
	b.tenantID = ""
	return b
}

// Call adds one op to the batch. action is the registered action code
// (e.g. "lead.__update", "myapp.actions.http_request"); params is the
// handler's params struct or a pre-built map[string]any.
//
// The builder marshals params via encoding/json so handler-typed param
// structs work without a separate map conversion step.
func (b *Batch) Call(action string, params any) *Batch {
	b.ops = append(b.ops, BatchOp{Action: action, Params: toParamsMap(params)})
	return b
}

// AddOp appends a fully-formed op verbatim - Action, ObjectIDs, and Params are
// sent exactly as given. Use it when a higher layer already holds built
// BatchOps (e.g. a service that assembles a heterogeneous transaction) instead
// of the typed .Create/.Upsert/.Update/.Delete/.Restore sugar. The typed
// helpers are themselves thin wrappers over AddOp, so the wire shape is
// identical either way.
func (b *Batch) AddOp(op BatchOp) *Batch {
	b.ops = append(b.ops, op)
	return b
}

// Create adds one __create op for `entity` with the given `fields` patch.
// Sugar for Call("<entity>.__create", {entity, fields}).
//
// `data.create` is one-row-per-dispatch; N independent inserts compose by
// calling .Create N times on the same batch.
func (b *Batch) Create(entity string, fields map[string]any) *Batch {
	return b.Call(fmt.Sprintf("%s.__create", entity), map[string]any{
		"entity": entity,
		"fields": fields,
	})
}

// Upsert adds one __upsert op for `entity` with the given `fields` patch,
// `uniqueBy` conflict keys, and optional `mode`. Sugar for
// Call("<entity>.__upsert", {entity, fields, unique_by, mode?}).
//
// mode is one of "upsert" (default, "" = upsert), "insert_if_missing",
// "insert", "replace" per declarion-core handler/data_handler.go. The
// platform validates the string; an unknown mode rolls the batch back.
func (b *Batch) Upsert(entity string, fields map[string]any, uniqueBy []string, mode string) *Batch {
	params := map[string]any{
		"entity":    entity,
		"fields":    fields,
		"unique_by": uniqueBy,
	}
	if mode != "" {
		params["mode"] = mode
	}
	return b.Call(fmt.Sprintf("%s.__upsert", entity), params)
}

// Update adds one __update op for `entity` applying the SAME `fields` patch to
// every row in `objectIDs`. The op carries object_ids at the TOP LEVEL (the
// dispatcher's PK channel); params holds only the non-PK body
// {entity, fields, condition?, error_if_not_found?}.
//
// `condition` (optional) is an expr-lang CAS guard evaluated server-side
// per row. `errorIfNotFound` (optional) flips zero-match from a silent
// no-op to a NOT_FOUND error that rolls the batch back.
//
// For N rows with DIFFERENT field patches, call .Update N times - one op per
// row, each with a single object_id and its own fields, all in one batch.
func (b *Batch) Update(entity string, objectIDs []string, fields map[string]any, opts ...UpdateOption) *Batch {
	cfg := updateConfig{}
	for _, opt := range opts {
		opt(&cfg)
	}
	params := map[string]any{
		"entity": entity,
		"fields": fields,
	}
	if cfg.condition != "" {
		params["condition"] = cfg.condition
	}
	if cfg.errorIfNotFound {
		params["error_if_not_found"] = true
	}
	return b.AddOp(BatchOp{
		Action:    fmt.Sprintf("%s.__update", entity),
		ObjectIDs: objectIDs,
		Params:    params,
	})
}

// Delete adds one __delete op for `entity` targeting the given object_ids
// (carried at the op top level, not in params), a DeleteWhere predicate, or
// both (an intersection). At least one MUST be present.
//
// Inside a batch the whole delete runs in the batch's transaction, so a
// predicate-addressed delete is atomic with the rest of the batch rather than
// draining in independent pages.
func (b *Batch) Delete(entity string, objectIDs []string, opts ...DeleteOption) *Batch {
	var options deleteOptions
	for _, opt := range opts {
		opt(&options)
	}
	params := map[string]any{"entity": entity}
	if len(options.filters) > 0 {
		params["filters"] = options.filters
	}
	return b.AddOp(BatchOp{
		Action:    fmt.Sprintf("%s.__delete", entity),
		ObjectIDs: objectIDs,
		Params:    params,
	})
}

// Restore adds one __restore op for `entity` targeting the given object_ids
// (carried at the op top level, not in params).
func (b *Batch) Restore(entity string, objectIDs []string) *Batch {
	return b.AddOp(BatchOp{
		Action:    fmt.Sprintf("%s.__restore", entity),
		ObjectIDs: objectIDs,
		Params:    map[string]any{"entity": entity},
	})
}

// Execute sends the accumulated ops to POST /api/actions/system.batch.
// Returns the parsed BatchResponse. A logical op failure surfaces as
// committed=false with the failed-op index in Results, NOT as a Go error;
// Go error returns reflect transport / infra failures only.
func (b *Batch) Execute(ctx context.Context) (*BatchResponse, error) {
	if len(b.ops) == 0 {
		return nil, fmt.Errorf("declarion: batch.Execute called with no ops; add ops via .Call / .Create / .Upsert / .Update / .Delete / .Restore")
	}
	body := map[string]any{
		"actions": b.ops,
		"atomic":  true,
	}
	respBody, status, contentType, err := b.c.do(ctx, "POST", "/api/actions/system.batch", nil, body, targetTenantOptions(b.tenantID, b.tenantCode)...)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, errorFromResponse(status, respBody, "/api/actions/system.batch", contentType)
	}
	// The HTTP layer wraps handler returns under a "result" envelope.
	var envelope struct {
		Status string         `json:"status"`
		Result *BatchResponse `json:"result"`
	}
	if err := json.Unmarshal(respBody, &envelope); err != nil {
		return nil, fmt.Errorf("unmarshal batch response: %w", err)
	}
	if envelope.Result == nil {
		return nil, fmt.Errorf("batch response missing result body: %s", string(respBody))
	}
	return envelope.Result, nil
}

// toParamsMap converts an arbitrary value to map[string]any via JSON
// round-trip. Pre-built maps pass through cheaply via type assertion.
func toParamsMap(v any) map[string]any {
	if v == nil {
		return nil
	}
	if m, ok := v.(map[string]any); ok {
		return m
	}
	b, err := json.Marshal(v)
	if err != nil {
		// JSON marshal failure on a params struct is a programmer error;
		// surface it at Execute via an empty-params op rather than panicking
		// inside the builder.
		return map[string]any{}
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return map[string]any{}
	}
	return m
}
