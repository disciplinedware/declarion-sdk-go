package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// DataClient wraps the platform data API surface. After the 2026-06-01
// write-API rewrite the only legacy CRUD endpoints left are reads:
//
//   - GET  /api/data/{entity}            — List + query-PK Get
//   - POST /api/data/{entity}/export     — CSV blob streaming (not in SDK yet)
//
// Every write (insert / upsert / update / delete / restore) dispatches
// through the 5 platform-default actions
// (POST /api/actions/{entity}.__{op}) with a FLAT request envelope:
//
//	{"object_ids": [...], "entity": "X", "fields": {...}, ...}
//
// See declarion-core plan 2026-06-01-write-api-final §1.2 and the source-of-
// truth parser at server/handlers/actions_body.go::parseActionBody. The legacy
// `_ids` key and the legacy CRUD write routes are rejected by the platform.
type DataClient struct {
	c *Client
	// request carries per-client request options (currently the target tenant)
	// onto every call this sub-client makes.
	request []RequestOption
}

// WithTargetTenantID returns a DataClient that sends X-Declarion-Tenant-ID on
// every call, so a cross-tenant service identity can read and write one
// tenant's data. Mirrors Batch.WithTargetTenantID and the Actions client: a
// data write is no less in need of a target tenant than an action is, and
// before this a caller who needed one had to wrap the write in a batch.
//
// Returns a copy; the receiver is unchanged.
func (d *DataClient) WithTargetTenantID(tenantID string) *DataClient {
	return &DataClient{c: d.c, request: []RequestOption{WithTargetTenantID(tenantID)}}
}

// WithTargetTenantCode is WithTargetTenantID by tenant code
// (X-Declarion-Tenant-Code). Mutually exclusive with it.
func (d *DataClient) WithTargetTenantCode(tenantCode string) *DataClient {
	return &DataClient{c: d.c, request: []RequestOption{WithTargetTenantCode(tenantCode)}}
}

// ListParams configures a List request. The server supports two pagination
// modes picked by which params are non-zero:
//
//   - Cursor mode (default, O(log n), scales to millions of rows): set Limit
//     and optionally After. Response includes Meta.HasMore and Meta.Cursor.
//     Recommended for UIs, infinite scroll, batch processing.
//
//   - Offset mode (classic page/per_page, supports "page 47 of 100" UIs): set
//     Page and PerPage. Response includes Meta.Total / Meta.TotalPages.
//     COUNT(*) runs - more expensive on large tables.
//
// Do not mix modes - setting both cursor and offset params is ambiguous.
// The server silently prefers cursor on conflict; the SDK matches that.

// CountMode selects how List computes the total row count, mirroring the
// platform's `count` query param (declarion-core data_list_params.go
// parseCountMode). The zero value (CountNone) returns no total.
type CountMode string

const (
	// CountNone returns no total (Meta.Total = 0); the page-window query alone runs.
	CountNone CountMode = ""
	// CountWith returns rows + a total computed with the ENTITY'S declared
	// strategy: an entity marked `count: estimated` answers from an O(1)
	// query-planner estimate, NOT an exact COUNT(*). Use for UI lists where an
	// approximate total is acceptable.
	CountWith CountMode = "with"
	// CountOnly returns the total ONLY (no row window) - same strategy as CountWith.
	CountOnly CountMode = "only"
	// CountExact returns rows + a FORCED exact COUNT(*), overriding an
	// `estimated` entity strategy. Use when the total must be precise (a
	// full table scan - more expensive on large tables).
	CountExact CountMode = "exact"
)

type ListParams struct {
	// Cursor mode.
	Limit int    // max rows per page; server clamps to 1-1000 (default 50).
	After string // opaque cursor from a prior response's Meta.Cursor; empty = first page.

	// Offset mode.
	Page    int
	PerPage int

	// Shared across modes.
	Sort    string       // field name; prefix "-" for descending; "$status.pipeline" for status sort.
	Search  string       // full-text search against entity's configured search_fields.
	Filters []FilterNode // structured filter tree (see filter.go). Serialized as JSON in `filters`.
	Select  []string     // field projection; empty = all columns.

	// Count selects the total-count strategy, mapping to the platform's `count`
	// query param. Empty (CountNone) returns no total (Meta.Total = 0). Cursor
	// mode omits count by default to save a query; set this when a total is
	// wanted. See CountMode for the modes - notably CountExact forces an exact
	// COUNT(*) even for an entity whose declared strategy is `estimated`, which
	// CountWith would otherwise answer from a planner estimate.
	Count CountMode

	// IncludeDeleted is permission-gated server-side (view_deleted). Silently
	// ignored without the permission.
	IncludeDeleted bool
}

// ListMeta is pagination metadata returned with a List response. Fields are
// mode-dependent: cursor mode populates HasMore/Cursor/Limit; offset mode
// populates Page/PerPage/Total/TotalPages. Total is also populated in cursor
// mode when ListParams.IncludeCount was true.
type ListMeta struct {
	Total      int64  `json:"total"`
	Limit      int    `json:"limit"`
	HasMore    bool   `json:"has_more"`
	Cursor     string `json:"cursor,omitempty"`
	Page       int    `json:"page,omitempty"`
	PerPage    int    `json:"per_page,omitempty"`
	TotalPages int    `json:"total_pages,omitempty"`
}

// ListResponse is the paginated response from GET /api/data/{entity}.
// The server envelope is {"data": [...], "meta": {...}, "$refs": {...}};
// this struct maps that envelope directly.
type ListResponse struct {
	Data []map[string]any `json:"data"`
	Meta ListMeta         `json:"meta"`
	// Refs carries expanded referenced entities (display-level resolution)
	// under {entityCode: {id: {row}}}. Absent when the response has no refs.
	Refs map[string]map[string]map[string]any `json:"$refs,omitempty"`
}

// Get retrieves a single record by primary key.
//
// Core has no path-style /{entity}/{id} read route. Single-record reads go
// through GET /api/data/{entity}: the List handler returns one record when
// the query carries every primary-key field of the entity (core data.go ->
// extractPKFromQuery -> entity.PrimaryKeyFields()), otherwise it lists.
//
// pk maps primary-key field names to values - {"id": "<uuid>"} for the
// standard single-id PK, or every field of a composite key. Every PK field
// MUST be present, or core falls through to list mode and the response
// shape will not match.
//
// The platform wraps the response in {"data": {...}}; this method unwraps it
// and returns the inner object directly.
func (d *DataClient) Get(ctx context.Context, entity string, pk map[string]any) (map[string]any, error) {
	if len(pk) == 0 {
		return nil, fmt.Errorf("data get %s: pk must contain at least one primary-key field", entity)
	}
	path := fmt.Sprintf("/api/data/%s", entity)
	q := url.Values{}
	for field, value := range pk {
		q.Set(field, fmt.Sprint(value))
	}
	body, status, err := d.c.do(ctx, "GET", path, q, nil, d.request...)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, &APIError{StatusCode: status, Body: string(body), Path: path}
	}
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("unmarshal get response: %w", err)
	}
	if envelope.Data == nil {
		return nil, fmt.Errorf("get response missing data field")
	}
	return envelope.Data, nil
}

// List retrieves records with pagination and filters.
//
// See ListParams for pagination-mode selection. Query params emitted:
//   - limit, after            - cursor mode
//   - page, per_page          - offset mode
//   - sort, search            - both modes
//   - filters                 - JSON-encoded []FilterNode (omitted when empty)
//   - select                  - comma-separated field list
//   - count=with|only|exact   - total-count strategy (ListParams.Count)
//   - include_deleted=true    - permission-gated soft-deleted rows
func (d *DataClient) List(ctx context.Context, entity string, params ListParams) (*ListResponse, error) {
	q := url.Values{}
	if params.Limit > 0 {
		q.Set("limit", strconv.Itoa(params.Limit))
	}
	if params.After != "" {
		q.Set("after", params.After)
	}
	if params.Page > 0 {
		q.Set("page", strconv.Itoa(params.Page))
	}
	if params.PerPage > 0 {
		q.Set("per_page", strconv.Itoa(params.PerPage))
	}
	if params.Sort != "" {
		q.Set("sort", params.Sort)
	}
	if params.Search != "" {
		q.Set("search", params.Search)
	}
	if len(params.Filters) > 0 {
		raw, err := json.Marshal(params.Filters)
		if err != nil {
			return nil, fmt.Errorf("marshal filters: %w", err)
		}
		q.Set("filters", string(raw))
	}
	if len(params.Select) > 0 {
		q.Set("select", strings.Join(params.Select, ","))
	}
	if params.Count != CountNone {
		// The platform reads the row-count strategy from `count` (modes
		// only/with/exact via parseCountMode). A legacy boolean `include_count`
		// is rejected as PARAM_UNKNOWN by the current platform.
		q.Set("count", string(params.Count))
	}
	if params.IncludeDeleted {
		q.Set("include_deleted", "true")
	}

	path := fmt.Sprintf("/api/data/%s", entity)
	body, status, err := d.c.do(ctx, "GET", path, q, nil, d.request...)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, &APIError{StatusCode: status, Body: string(body), Path: path}
	}
	var result ListResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("unmarshal list response: %w", err)
	}
	return &result, nil
}

// ----------------------------------------------------------------------------
// Write API — FLAT action envelope (declarion-core 2026-06-01 write-API).
// ----------------------------------------------------------------------------
//
// Every write dispatches POST /api/actions/{entity}.__{op} with a flat body:
//
//	{
//	  "object_ids": ["uuid-1", ...],        // omitted for __create / __upsert
//	  "entity": "lead",
//	  "fields": {"name": "...", ...},        // omitted for __delete / __restore
//	  "unique_by": ["email"],                // __upsert only
//	  "mode": "upsert",                      // __upsert only
//	  "condition": "entity.status == 'X'",   // __update only (CAS guard)
//	  "error_if_not_found": false            // __update only
//	}
//
// `object_ids` is the only reserved top-level key. Every other top-level key
// is parsed by parseActionBody (declarion-core
// server/handlers/actions_body.go) directly into the handler's typed params
// struct via JSON tags. There is NO `params:` wrapper on the wire.
//
// Per-row variation (N different `fields` patches in one call) is expressed
// as N actions in one batched dispatch — see Batch.{Create,Update,Upsert,
// Delete,Restore} in batch.go. The single-dispatch helpers below apply the
// same `fields` patch to every object_id.

// updateConfig is the resolved set of __update envelope flags collected from
// UpdateOption functional options.
type updateConfig struct {
	condition       string
	errorIfNotFound bool
}

// UpdateOption tunes one BulkUpdate call. Maps onto the __update envelope's
// optional flags (`condition`, `error_if_not_found`).
type UpdateOption func(*updateConfig)

// WithCondition sets the optional CAS-guard predicate evaluated server-side
// against each row's pre-update state (expr-lang/expr syntax against
// `entity`, `event`, `now`). A row whose condition evaluates false is
// skipped, not failed.
func WithCondition(condition string) UpdateOption {
	return func(c *updateConfig) { c.condition = condition }
}

// WithErrorIfNotFound makes BulkUpdate fail with NOT_FOUND when zero rows
// match the PK + condition gate. Default behavior is silent no-op.
func WithErrorIfNotFound(errorIfNotFound bool) UpdateOption {
	return func(c *updateConfig) { c.errorIfNotFound = errorIfNotFound }
}

// upsertConfig is the resolved set of __upsert envelope flags collected from
// UpsertOption functional options.
type upsertConfig struct {
	mode string
}

// UpsertOption tunes one BulkUpsert call. Maps onto the __upsert envelope's
// optional `mode` flag.
type UpsertOption func(*upsertConfig)

// WithMode sets the upsert mode flag. Supported values per
// declarion-core handler/data_handler.go: "upsert" (default),
// "insert_if_missing", "insert", "replace". The platform validates the
// string; an unknown mode surfaces as an APIError.
func WithMode(mode string) UpsertOption {
	return func(c *upsertConfig) { c.mode = mode }
}

// BulkCreateResult is the unwrapped result of a BulkCreate call. Rows is
// the inserted row in input order. Per the __create handler contract one
// row is inserted per dispatch; the slice exists so the SDK shape matches
// every other Bulk* result (and forward-compatibility if the handler ever
// accepts multiple).
type BulkCreateResult struct {
	Rows             []map[string]any
	AuditOperationID string
}

// BulkUpsertResult is the unwrapped result of a BulkUpsert call. Each row
// carries the per-row outcome from the platform `data.upsert` handler.
type BulkUpsertResult struct {
	Rows             []UpsertRowResult
	AuditOperationID string
}

// UpsertRowResult is one row's outcome from a BulkUpsert call. Mirrors the
// platform's DataUpsertRowResult shape.
type UpsertRowResult struct {
	// PK is the row's primary-key value (single-column PK string, or
	// composite PK joined by U+001F per store.ObjectID).
	PK string `json:"pk"`
	// Action is one of "inserted", "updated", "skipped_noop".
	Action string `json:"action"`
	// Row is the post-upsert row state. Absent for skipped_noop.
	Row map[string]any `json:"row,omitempty"`
}

// BulkUpdateResult is the unwrapped result of a BulkUpdate call. Rows
// carries the post-update row state in object_ids order; nil entries mark
// rows skipped by the condition gate. RowsMatched is the number of rows
// that actually wrote.
type BulkUpdateResult struct {
	Rows             []map[string]any
	RowsMatched      int
	AuditOperationID string
}

// BulkDeleteResult is the unwrapped result of a BulkDelete call.
type BulkDeleteResult struct {
	Deleted          int
	AuditOperationID string
}

// BulkRestoreResult is the unwrapped result of a BulkRestore call.
type BulkRestoreResult struct {
	Restored         int
	AuditOperationID string
}

// BulkCreate inserts one row via POST /api/actions/{entity}.__create.
//
// The platform `data.create` handler accepts a single `fields` map per
// dispatch (one INSERT per call). For N independent inserts use
// Batch.Create — one __create op per row inside a single transactional
// system.batch.
//
// Subresources ($properties, $statuses, $children, $params, $files) are
// passed as `$`-prefixed keys INSIDE `fields`; the dispatcher extracts
// them via subresource.ExtractSubResources. There is no separate
// "subresources" envelope slot.
func (d *DataClient) BulkCreate(ctx context.Context, entity string, fields map[string]any) (BulkCreateResult, error) {
	if entity == "" {
		return BulkCreateResult{}, fmt.Errorf("data create: entity is required")
	}
	if len(fields) == 0 {
		return BulkCreateResult{}, fmt.Errorf("data create %s: fields must be non-empty", entity)
	}
	path := fmt.Sprintf("/api/actions/%s.__create", entity)
	body := map[string]any{
		"entity": entity,
		"fields": fields,
	}
	envelope, err := d.dispatchWrite(ctx, path, body)
	if err != nil {
		return BulkCreateResult{}, err
	}
	var result struct {
		Rows []map[string]any `json:"rows"`
	}
	if err := decodeActionResult(envelope.Result, &result); err != nil {
		return BulkCreateResult{}, fmt.Errorf("decode create result: %w", err)
	}
	return BulkCreateResult{Rows: result.Rows, AuditOperationID: envelope.AuditOperationID}, nil
}

// BulkUpsert applies one `fields` patch via POST /api/actions/{entity}.__upsert.
//
// The platform `data.upsert` handler matches an existing row by `uniqueBy`
// columns: when present, the row is updated; when absent, a new row is
// inserted (subject to `mode`). `uniqueBy` is required by the handler.
//
// For N rows with DIFFERENT field maps, use Batch.Upsert — one __upsert op
// per row inside a single transactional system.batch.
func (d *DataClient) BulkUpsert(ctx context.Context, entity string, fields map[string]any, uniqueBy []string, opts ...UpsertOption) (BulkUpsertResult, error) {
	if entity == "" {
		return BulkUpsertResult{}, fmt.Errorf("data upsert: entity is required")
	}
	if len(fields) == 0 {
		return BulkUpsertResult{}, fmt.Errorf("data upsert %s: fields must be non-empty", entity)
	}
	if len(uniqueBy) == 0 {
		return BulkUpsertResult{}, fmt.Errorf("data upsert %s: unique_by must be non-empty", entity)
	}
	cfg := upsertConfig{}
	for _, opt := range opts {
		opt(&cfg)
	}
	path := fmt.Sprintf("/api/actions/%s.__upsert", entity)
	body := map[string]any{
		"entity":    entity,
		"fields":    fields,
		"unique_by": uniqueBy,
	}
	if cfg.mode != "" {
		body["mode"] = cfg.mode
	}
	envelope, err := d.dispatchWrite(ctx, path, body)
	if err != nil {
		return BulkUpsertResult{}, err
	}
	var result struct {
		Rows []UpsertRowResult `json:"rows"`
	}
	if err := decodeActionResult(envelope.Result, &result); err != nil {
		return BulkUpsertResult{}, fmt.Errorf("decode upsert result: %w", err)
	}
	return BulkUpsertResult{Rows: result.Rows, AuditOperationID: envelope.AuditOperationID}, nil
}

// BulkUpdate applies one `fields` patch to every object_id via
// POST /api/actions/{entity}.__update.
//
// Single-dispatch semantics: the same patch goes to every targeted row.
// For per-row variation (N different patches), use Batch.Update — N
// __update ops in one transactional system.batch.
//
// `objectIDs` must be entity primary keys; composite PKs are encoded as a
// single string by joining the PK fields in declaration order via U+001F
// (callers using composite PKs must pre-encode via the platform's
// store.ObjectID convention - this SDK does not encode them here because
// Go map iteration order would be non-deterministic).
//
// `condition` (WithCondition) is an optional CAS guard evaluated server-
// side; rows failing the predicate are skipped, not erred.
// `error_if_not_found` (WithErrorIfNotFound) flips zero-match from a
// silent no-op to a NOT_FOUND error.
func (d *DataClient) BulkUpdate(ctx context.Context, entity string, objectIDs []string, fields map[string]any, opts ...UpdateOption) (BulkUpdateResult, error) {
	if entity == "" {
		return BulkUpdateResult{}, fmt.Errorf("data update: entity is required")
	}
	if len(objectIDs) == 0 {
		return BulkUpdateResult{}, fmt.Errorf("data update %s: object_ids must be non-empty", entity)
	}
	if len(fields) == 0 {
		return BulkUpdateResult{}, fmt.Errorf("data update %s: fields must be non-empty", entity)
	}
	cfg := updateConfig{}
	for _, opt := range opts {
		opt(&cfg)
	}
	path := fmt.Sprintf("/api/actions/%s.__update", entity)
	body := map[string]any{
		"object_ids": objectIDs,
		"entity":     entity,
		"fields":     fields,
	}
	if cfg.condition != "" {
		body["condition"] = cfg.condition
	}
	if cfg.errorIfNotFound {
		body["error_if_not_found"] = true
	}
	envelope, err := d.dispatchWrite(ctx, path, body)
	if err != nil {
		return BulkUpdateResult{}, err
	}
	var result struct {
		Rows        []map[string]any `json:"rows"`
		RowsMatched int              `json:"rows_matched"`
	}
	if err := decodeActionResult(envelope.Result, &result); err != nil {
		return BulkUpdateResult{}, fmt.Errorf("decode update result: %w", err)
	}
	return BulkUpdateResult{
		Rows:             result.Rows,
		RowsMatched:      result.RowsMatched,
		AuditOperationID: envelope.AuditOperationID,
	}, nil
}

// DeleteOption configures a BulkDelete call. Options are variadic so the
// existing three-argument BulkDelete call sites keep compiling unchanged.
type DeleteOption func(*deleteOptions)

type deleteOptions struct {
	filters []FilterNode
}

// DeleteWhere addresses rows by PREDICATE instead of (or in addition to) by
// id, using the same filter grammar as a List read. The platform resolves the
// predicate to primary keys under the caller's DELETE access scope and deletes
// them through the normal per-row path - hooks, audit before-images and file
// cascades all still run.
//
//	// every result row of one backtest
//	client.BulkDelete(ctx, "arm_result", nil,
//	    platform.DeleteWhere(
//	        platform.Eq("backtest_id", id),
//	        platform.Eq("execution_origin", "backtest"),
//	    ))
//
// Passed together with object_ids it is a GUARD - "delete these ids, but only
// while they still match" - and the call then costs O(len(objectIDs)) however
// many rows the predicate would match on its own.
//
// The server rejects a filter it cannot apply in full (an unknown field, an
// operator with no value) rather than dropping the condition: on a read a
// dropped condition merely widens what you see, on a delete it would widen what
// you destroy.
func DeleteWhere(filters ...FilterNode) DeleteOption {
	return func(o *deleteOptions) { o.filters = append(o.filters, filters...) }
}

// BulkDelete deletes rows via POST /api/actions/{entity}.__delete.
//
// Rows are addressed by objectIDs, by a DeleteWhere predicate, or by both (an
// intersection). At least one MUST be present: a delete that addresses nothing
// is an error, never a table wipe. Deleting is soft or hard per the entity's
// declaration.
//
// Composite-PK encoding matches BulkUpdate: callers pre-encode composite
// PKs via the platform U+001F convention.
func (d *DataClient) BulkDelete(ctx context.Context, entity string, objectIDs []string, opts ...DeleteOption) (BulkDeleteResult, error) {
	if entity == "" {
		return BulkDeleteResult{}, fmt.Errorf("data delete: entity is required")
	}
	var options deleteOptions
	for _, opt := range opts {
		opt(&options)
	}
	if len(objectIDs) == 0 && len(options.filters) == 0 {
		return BulkDeleteResult{}, fmt.Errorf("data delete %s: object_ids or filters must be non-empty", entity)
	}
	path := fmt.Sprintf("/api/actions/%s.__delete", entity)
	body := map[string]any{
		"entity": entity,
	}
	if len(objectIDs) > 0 {
		body["object_ids"] = objectIDs
	}
	if len(options.filters) > 0 {
		body["filters"] = options.filters
	}
	envelope, err := d.dispatchWrite(ctx, path, body)
	if err != nil {
		return BulkDeleteResult{}, err
	}
	var result struct {
		Deleted int `json:"deleted"`
	}
	if err := decodeActionResult(envelope.Result, &result); err != nil {
		return BulkDeleteResult{}, fmt.Errorf("decode delete result: %w", err)
	}
	return BulkDeleteResult{Deleted: result.Deleted, AuditOperationID: envelope.AuditOperationID}, nil
}

// BulkRestore un-soft-deletes rows by object_id via
// POST /api/actions/{entity}.__restore.
func (d *DataClient) BulkRestore(ctx context.Context, entity string, objectIDs []string) (BulkRestoreResult, error) {
	if entity == "" {
		return BulkRestoreResult{}, fmt.Errorf("data restore: entity is required")
	}
	if len(objectIDs) == 0 {
		return BulkRestoreResult{}, fmt.Errorf("data restore %s: object_ids must be non-empty", entity)
	}
	path := fmt.Sprintf("/api/actions/%s.__restore", entity)
	body := map[string]any{
		"object_ids": objectIDs,
		"entity":     entity,
	}
	envelope, err := d.dispatchWrite(ctx, path, body)
	if err != nil {
		return BulkRestoreResult{}, err
	}
	var result struct {
		Restored int `json:"restored"`
	}
	if err := decodeActionResult(envelope.Result, &result); err != nil {
		return BulkRestoreResult{}, fmt.Errorf("decode restore result: %w", err)
	}
	return BulkRestoreResult{Restored: result.Restored, AuditOperationID: envelope.AuditOperationID}, nil
}

// actionEnvelope is the wire shape every /api/actions/{code} success
// response wraps the handler's typed Result into. See
// declarion-core server/handlers/actions_dispatch.go:139-159.
type actionEnvelope struct {
	Status           string          `json:"status"`
	Result           json.RawMessage `json:"result,omitempty"`
	AuditOperationID string          `json:"audit_operation_id,omitempty"`
	ObjectCount      int             `json:"object_count,omitempty"`
}

// dispatchWrite POSTs the FLAT action body and returns the parsed action
// envelope. Non-2xx surfaces as APIError with body truncated.
func (d *DataClient) dispatchWrite(ctx context.Context, path string, body map[string]any) (actionEnvelope, error) {
	respBody, status, err := d.c.do(ctx, "POST", path, nil, body, d.request...)
	if err != nil {
		return actionEnvelope{}, err
	}
	if status < 200 || status >= 300 {
		return actionEnvelope{}, &APIError{StatusCode: status, Body: truncate(string(respBody), 500), Path: path}
	}
	var envelope actionEnvelope
	if err := json.Unmarshal(respBody, &envelope); err != nil {
		return actionEnvelope{}, fmt.Errorf("unmarshal action response: %w", err)
	}
	return envelope, nil
}

// decodeActionResult unmarshals the handler-typed `result` field into the
// caller's struct. A nil/absent result decodes to the zero value, which is
// the right answer when the handler returned no body (e.g. an error path
// that still produced a 2xx envelope).
func decodeActionResult(raw json.RawMessage, into any) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	return json.Unmarshal(raw, into)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return strings.TrimSpace(s[:max]) + "..."
}
