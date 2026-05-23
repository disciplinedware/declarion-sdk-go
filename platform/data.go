package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// DataClient wraps /api/data/{entity} endpoints.
type DataClient struct {
	c *Client
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

	// IncludeCount opts into COUNT(*). Cursor mode omits count by default to
	// save a query; set this true when the UI wants a total. Offset mode
	// runs count unconditionally.
	IncludeCount bool

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
	body, status, err := d.c.do(ctx, "GET", path, q, nil)
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
//   - include_count=true      - opt-in count in cursor mode
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
	if params.IncludeCount {
		q.Set("include_count", "true")
	}
	if params.IncludeDeleted {
		q.Set("include_deleted", "true")
	}

	path := fmt.Sprintf("/api/data/%s", entity)
	body, status, err := d.c.do(ctx, "GET", path, q, nil)
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

// Create creates records. Accepts a slice of records.
func (d *DataClient) Create(ctx context.Context, entity string, records []map[string]any) ([]map[string]any, error) {
	return d.writeMany(ctx, "POST", entity, "", records)
}

// Update updates records by primary key. Accepts a slice of records with
// PK fields plus the new values included in each item.
//
// Routes through the platform-default __update action introduced by
// declarion-core's unified-action-toolbar migration: the legacy CRUD path
// (PATCH /api/data/{entity}) is retired in favor of the action API so that
// permission gating, ABAC, audit, and lifecycle events flow through a
// single chokepoint (dispatch_update.go). The request body is the
// data.update handler shape: {entity, items}; the response envelope is
// {status, result: {rows, rows_matched}, audit_operation_id} and this
// method unwraps result.rows for the caller.
func (d *DataClient) Update(ctx context.Context, entity string, records []map[string]any) ([]map[string]any, error) {
	if entity == "" {
		return nil, fmt.Errorf("data update: entity is required")
	}
	path := fmt.Sprintf("/api/actions/%s.__update", entity)
	reqBody := map[string]any{
		"entity": entity,
		"items":  records,
	}
	body, status, err := d.c.do(ctx, "POST", path, nil, reqBody)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, &APIError{StatusCode: status, Body: truncate(string(body), 500), Path: path}
	}
	var envelope struct {
		Status string `json:"status"`
		Result struct {
			Rows        []map[string]any `json:"rows"`
			RowsMatched int              `json:"rows_matched"`
		} `json:"result"`
		AuditOperationID string `json:"audit_operation_id,omitempty"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("unmarshal update response: %w", err)
	}
	return envelope.Result.Rows, nil
}

// Delete soft-deletes records by PK objects.
//
// Routes through the platform-default __delete action; the legacy CRUD
// path (POST /api/data/{entity}/delete) was retired and now returns 410
// Gone. The action contract takes object IDs in the reserved `_ids`
// control key. Composite primary keys collapse into a single opaque ID
// using the platform Unit-Separator (U+001F) join, matching
// store.SplitObjectID on the server and encodeObjectId in the TS SDK.
// Each pkObject's iteration order must match the entity's declared
// PrimaryKeyFields() order.
func (d *DataClient) Delete(ctx context.Context, entity string, pkObjects []map[string]any) error {
	if entity == "" {
		return fmt.Errorf("data delete: entity is required")
	}
	ids := make([]string, len(pkObjects))
	for i, pk := range pkObjects {
		id, err := encodeObjectID(pk)
		if err != nil {
			return fmt.Errorf("data delete %s: pkObjects[%d]: %w", entity, i, err)
		}
		ids[i] = id
	}
	path := fmt.Sprintf("/api/actions/%s.__delete", entity)
	body, status, err := d.c.do(ctx, "POST", path, nil, map[string]any{"_ids": ids})
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return &APIError{StatusCode: status, Body: truncate(string(body), 500), Path: path}
	}
	return nil
}

// encodeObjectID flattens a primary-key map into the platform's opaque
// object-ID string. Single-field PK = bare value verbatim; composite PK =
// values joined with U+001F (ASCII Unit Separator), matching the Go-side
// store.ObjectID join and the TS-side encodeObjectId helper. Iteration
// order over the input must match the entity's PrimaryKeyFields()
// declaration order - callers using composite PKs MUST construct the map
// field-by-field in declaration order (Go's map iteration is randomized,
// but we walk the map and join in insertion order via the platform
// convention that single-field PK is the common case). For composite
// PKs callers SHOULD pass an ordered structure; here we accept the
// map but require exactly one entry for safety, otherwise we fail
// loudly rather than silently emitting a randomized order.
func encodeObjectID(pk map[string]any) (string, error) {
	if len(pk) == 0 {
		return "", fmt.Errorf("pk must contain at least one field")
	}
	if len(pk) == 1 {
		for _, v := range pk {
			return fmt.Sprint(v), nil
		}
	}
	// Composite PK: Go maps have randomized iteration order, so we cannot
	// reliably reconstruct the entity's declared PK order from a plain
	// map. Reject rather than emit a non-deterministic ID that would
	// mismatch store.SplitObjectID server-side. Callers with composite
	// PKs should pre-encode their IDs via a strongly-typed helper.
	return "", fmt.Errorf("composite primary keys (%d fields) cannot be encoded from an unordered map; pre-encode the object id", len(pk))
}

// UpsertItem is a single row returned by BulkUpsert. Fields contains all
// entity columns plus enrichment keys ($refs, $statuses, etc.).
// WasInserted is true when the row was created by this call (xmax = 0 in
// Postgres), false when an existing row was updated or left unchanged.
type UpsertItem struct {
	Fields      map[string]any
	WasInserted bool
}

// BulkUpsert creates or updates records using unique_fields for dedup.
// uniqueFields is a comma-separated list of fields (e.g. "id" or "email,tenant_id").
// conflictPredicate is an optional SQL WHERE clause for partial-index upserts
// (e.g. "linkedin IS NOT NULL AND deleted_at IS NULL"). Pass "" for full unique constraints.
func (d *DataClient) BulkUpsert(ctx context.Context, entity string, uniqueFields string, records []map[string]any, conflictPredicate ...string) ([]UpsertItem, error) {
	q := url.Values{}
	if uniqueFields != "" {
		q.Set("unique_fields", uniqueFields)
	}
	if len(conflictPredicate) > 0 && conflictPredicate[0] != "" {
		q.Set("conflict_predicate", conflictPredicate[0])
	}

	path := fmt.Sprintf("/api/data/%s", entity)
	u := path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}

	body, status, err := d.c.do(ctx, "POST", path, q, records)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, &APIError{StatusCode: status, Body: truncate(string(body), 500), Path: u}
	}
	var raw []map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("unmarshal upsert response: %w", err)
	}
	items := make([]UpsertItem, len(raw))
	for i, row := range raw {
		wasInserted, _ := row["was_inserted"].(bool)
		// Remove the synthetic field so Fields contains only entity data.
		delete(row, "was_inserted")
		items[i] = UpsertItem{Fields: row, WasInserted: wasInserted}
	}
	return items, nil
}

func (d *DataClient) writeMany(ctx context.Context, method, entity, queryExtra string, records []map[string]any) ([]map[string]any, error) {
	path := fmt.Sprintf("/api/data/%s", entity)
	var q url.Values
	if queryExtra != "" {
		q = url.Values{}
		q.Set("extra", queryExtra)
	}
	body, status, err := d.c.do(ctx, method, path, q, records)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, &APIError{StatusCode: status, Body: truncate(string(body), 500), Path: path}
	}
	var result []map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("unmarshal write response: %w", err)
	}
	return result, nil
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return strings.TrimSpace(s[:max]) + "..."
}
