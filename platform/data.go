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

// Get retrieves a single record by ID.
// The platform wraps the response in {"data": {...}}; this method unwraps it
// and returns the inner object directly.
func (d *DataClient) Get(ctx context.Context, entity, id string) (map[string]any, error) {
	path := fmt.Sprintf("/api/data/%s/%s", entity, id)
	body, status, err := d.c.do(ctx, "GET", path, nil, nil)
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

// Update updates records. Accepts a slice of records with PK fields included.
// Uses PATCH (core route: PATCH /api/data/{entity}).
func (d *DataClient) Update(ctx context.Context, entity string, records []map[string]any) ([]map[string]any, error) {
	return d.writeMany(ctx, "PATCH", entity, "", records)
}

// Delete soft-deletes records by PK objects.
// Uses POST /api/data/{entity}/delete (core convention: not HTTP DELETE).
func (d *DataClient) Delete(ctx context.Context, entity string, pkObjects []map[string]any) error {
	path := fmt.Sprintf("/api/data/%s/delete", entity)
	body, status, err := d.c.do(ctx, "POST", path, nil, pkObjects)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return &APIError{StatusCode: status, Body: string(body), Path: path}
	}
	return nil
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
