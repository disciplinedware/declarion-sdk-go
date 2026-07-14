package platform

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// TestList_unmarshal_envelope proves the SDK decodes the real server envelope
// {"data":[...],"meta":{...},"$refs":{...}} — the pre-fix shape had items/total
// at the top level, which silently returned empty lists for every caller.
func TestList_unmarshal_envelope(t *testing.T) {
	body := `{
		"data": [
			{"id": "a", "name": "Alice"},
			{"id": "b", "name": "Bob"}
		],
		"meta": {
			"total": 2,
			"limit": 50,
			"has_more": false,
			"cursor": "",
			"page": 1,
			"per_page": 50,
			"total_pages": 1
		},
		"$refs": {
			"company": {
				"c1": {"id": "c1", "name": "Acme"}
			}
		}
	}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	c := New(Config{BaseURL: srv.URL})
	resp, err := c.Data().List(t.Context(), "lead", ListParams{Limit: 50})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(resp.Data) != 2 {
		t.Fatalf("Data len: got %d, want 2", len(resp.Data))
	}
	if resp.Data[0]["name"] != "Alice" {
		t.Errorf("Data[0].name: got %v, want Alice", resp.Data[0]["name"])
	}
	if resp.Meta.Total != 2 {
		t.Errorf("Meta.Total: got %d, want 2", resp.Meta.Total)
	}
	if resp.Meta.Limit != 50 {
		t.Errorf("Meta.Limit: got %d, want 50", resp.Meta.Limit)
	}
	if resp.Meta.HasMore {
		t.Errorf("Meta.HasMore: got true, want false")
	}
	if resp.Refs == nil || resp.Refs["company"]["c1"]["name"] != "Acme" {
		t.Errorf("Refs: missing expected expansion, got %+v", resp.Refs)
	}
}

// TestList_cursor_meta verifies cursor-mode fields populate on HasMore pages.
func TestList_cursor_meta(t *testing.T) {
	body := `{
		"data": [{"id": "x"}],
		"meta": {"limit": 1, "has_more": true, "cursor": "eyJ0b2tlbiI6ICJhYmMifQ=="}
	}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	c := New(Config{BaseURL: srv.URL})
	resp, err := c.Data().List(t.Context(), "lead", ListParams{Limit: 1})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if !resp.Meta.HasMore {
		t.Error("Meta.HasMore: want true")
	}
	if resp.Meta.Cursor == "" {
		t.Error("Meta.Cursor: want non-empty")
	}
}

// TestList_query_params asserts every ListParams field lands on the wire with
// the expected key. Drift here silently breaks server-side parsing.
func TestList_query_params(t *testing.T) {
	var captured url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r.URL.Query()
		_, _ = w.Write([]byte(`{"data":[],"meta":{}}`))
	}))
	t.Cleanup(srv.Close)

	c := New(Config{BaseURL: srv.URL})
	_, err := c.Data().List(t.Context(), "lead", ListParams{
		Limit:          50,
		After:          "cursor-abc",
		Sort:           "-created_at",
		Search:         "acme",
		Select:         []string{"id", "name", "company_id"},
		Count:          CountWith,
		IncludeDeleted: true,
		Filters: []FilterNode{
			IsEmpty("company_id"),
			{Field: "score", Op: OpGte, Value: float64(70)},
		},
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	want := map[string]string{
		"limit":           "50",
		"after":           "cursor-abc",
		"sort":            "-created_at",
		"search":          "acme",
		"select":          "id,name,company_id",
		"count":           "with",
		"include_deleted": "true",
	}
	for k, v := range want {
		if got := captured.Get(k); got != v {
			t.Errorf("query[%s]: got %q, want %q", k, got, v)
		}
	}
	// Regression: the legacy boolean param must never be emitted - the platform
	// rejects it as PARAM_UNKNOWN. IncludeCount maps to count=with.
	if captured.Has("include_count") {
		t.Errorf("legacy include_count must not be emitted; got %q", captured.Get("include_count"))
	}

	rawFilters := captured.Get("filters")
	if rawFilters == "" {
		t.Fatal("query[filters]: missing")
	}
	var parsed []FilterNode
	if err := json.Unmarshal([]byte(rawFilters), &parsed); err != nil {
		t.Fatalf("query[filters] not JSON: %v (raw=%s)", err, rawFilters)
	}
	if len(parsed) != 2 {
		t.Fatalf("filters: got %d nodes, want 2", len(parsed))
	}
	if parsed[0].Field != "company_id" || parsed[0].Op != OpIsEmpty {
		t.Errorf("filters[0]: got %+v, want IsEmpty(company_id)", parsed[0])
	}
}

// TestList_omits_empty_params — a zero-value ListParams must not emit any
// pagination / filter keys. Keeps server-side parsing clean and matches "no
// params means platform default."
func TestList_omits_empty_params(t *testing.T) {
	var captured url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r.URL.Query()
		_, _ = w.Write([]byte(`{"data":[],"meta":{}}`))
	}))
	t.Cleanup(srv.Close)

	c := New(Config{BaseURL: srv.URL})
	_, err := c.Data().List(t.Context(), "lead", ListParams{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	forbidden := []string{"limit", "after", "page", "per_page", "sort", "search", "filters", "select", "count", "include_count", "include_deleted"}
	for _, k := range forbidden {
		if _, ok := captured[k]; ok {
			t.Errorf("empty ListParams must not emit %q (got %q)", k, captured.Get(k))
		}
	}
}

// TestList_offset_mode_params — page/per_page emit correctly and coexist
// with sort/search.
func TestList_offset_mode_params(t *testing.T) {
	var captured url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r.URL.Query()
		_, _ = w.Write([]byte(`{"data":[],"meta":{"page":3,"per_page":20,"total":57,"total_pages":3}}`))
	}))
	t.Cleanup(srv.Close)

	c := New(Config{BaseURL: srv.URL})
	resp, err := c.Data().List(t.Context(), "lead", ListParams{Page: 3, PerPage: 20, Sort: "name"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if captured.Get("page") != "3" {
		t.Errorf("page: got %q, want 3", captured.Get("page"))
	}
	if captured.Get("per_page") != "20" {
		t.Errorf("per_page: got %q, want 20", captured.Get("per_page"))
	}
	if resp.Meta.Page != 3 || resp.Meta.PerPage != 20 || resp.Meta.TotalPages != 3 {
		t.Errorf("offset meta: got %+v", resp.Meta)
	}
}

// TestList_http_error surfaces non-2xx as APIError with body preserved.
func TestList_http_error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(422)
		_, _ = w.Write([]byte(`{"error":{"code":"VALIDATION","message":"bad op"}}`))
	}))
	t.Cleanup(srv.Close)

	c := New(Config{BaseURL: srv.URL})
	_, err := c.Data().List(t.Context(), "lead", ListParams{
		Filters: []FilterNode{{Field: "x", Op: "pwn"}},
	})
	if err == nil {
		t.Fatal("List: want error on 422, got nil")
	}
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("err type: got %T, want *APIError", err)
	}
	if apiErr.StatusCode != 422 {
		t.Errorf("status: got %d, want 422", apiErr.StatusCode)
	}
	if !strings.Contains(apiErr.Body, "VALIDATION") {
		t.Errorf("body not preserved: %q", apiErr.Body)
	}
}

// captureWrite records the HTTP method, path, and JSON-decoded body of a
// single inbound action dispatch. Test helper for the 5 Bulk* assertions
// below: each one needs to pin the URL + envelope shape on the wire.
type captureWrite struct {
	method string
	path   string
	body   map[string]any
}

func newCaptureServer(t *testing.T, responseJSON string) (*httptest.Server, *captureWrite) {
	t.Helper()
	cap := &captureWrite{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.method = r.Method
		cap.path = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &cap.body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(responseJSON))
	}))
	t.Cleanup(srv.Close)
	return srv, cap
}

// assertFlatNoLegacy fails the test if the body contains any of the keys
// that the 2026-06-01 FLAT envelope explicitly forbids on the wire.
// Catches accidental regressions to the old `_ids` / `items` / `records`
// / `params:`-wrapper shapes the platform now rejects (or silently ignores).
func assertFlatNoLegacy(t *testing.T, body map[string]any) {
	t.Helper()
	for _, k := range []string{"_ids", "items", "records", "params"} {
		if _, ok := body[k]; ok {
			t.Errorf("FLAT envelope must not carry %q (got %+v)", k, body)
		}
	}
}

// TestBulkCreate_flat_envelope pins the wire shape:
//   - POST /api/actions/{entity}.__create
//   - body = {"entity": "<entity>", "fields": {...}}
//   - no `params:` wrapper, no `object_ids` (PKs generated server-side)
func TestBulkCreate_flat_envelope(t *testing.T) {
	srv, cap := newCaptureServer(t, `{
		"status": "success",
		"result": {"rows": [{"id": "u1", "name": "Alice"}]},
		"audit_operation_id": "op-create"
	}`)

	c := New(Config{BaseURL: srv.URL})
	res, err := c.Data().BulkCreate(t.Context(), "lead", map[string]any{"name": "Alice"})
	if err != nil {
		t.Fatalf("BulkCreate: %v", err)
	}

	if cap.method != "POST" {
		t.Errorf("method: got %q, want POST", cap.method)
	}
	if cap.path != "/api/actions/lead.__create" {
		t.Errorf("path: got %q, want /api/actions/lead.__create", cap.path)
	}
	if got, _ := cap.body["entity"].(string); got != "lead" {
		t.Errorf("body.entity: got %q, want lead", got)
	}
	fields, ok := cap.body["fields"].(map[string]any)
	if !ok {
		t.Fatalf("body.fields: got %T, want map", cap.body["fields"])
	}
	if fields["name"] != "Alice" {
		t.Errorf("body.fields.name: got %v, want Alice", fields["name"])
	}
	if _, ok := cap.body["object_ids"]; ok {
		t.Errorf("__create must not carry object_ids (PKs are generated): got %+v", cap.body)
	}
	assertFlatNoLegacy(t, cap.body)

	if len(res.Rows) != 1 || res.Rows[0]["id"] != "u1" {
		t.Errorf("rows: got %+v", res.Rows)
	}
	if res.AuditOperationID != "op-create" {
		t.Errorf("audit_operation_id: got %q", res.AuditOperationID)
	}
}

// TestBulkCreate_rejects_empty_inputs guards programmer errors before
// hitting the server.
func TestBulkCreate_rejects_empty_inputs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("server must not be hit on empty-input rejection")
	}))
	t.Cleanup(srv.Close)
	c := New(Config{BaseURL: srv.URL})
	if _, err := c.Data().BulkCreate(t.Context(), "", map[string]any{"x": 1}); err == nil {
		t.Error("BulkCreate with empty entity: want error")
	}
	if _, err := c.Data().BulkCreate(t.Context(), "lead", nil); err == nil {
		t.Error("BulkCreate with empty fields: want error")
	}
}

// TestBulkUpsert_flat_envelope pins:
//   - POST /api/actions/{entity}.__upsert
//   - body = {"entity", "fields", "unique_by", "mode"?}
//   - no object_ids, no `params:` wrapper, no legacy `records`/`items`
func TestBulkUpsert_flat_envelope(t *testing.T) {
	srv, cap := newCaptureServer(t, `{
		"status": "success",
		"result": {
			"rows": [
				{"pk": "u1", "action": "inserted", "row": {"id": "u1", "email": "a@x"}}
			]
		}
	}`)

	c := New(Config{BaseURL: srv.URL})
	res, err := c.Data().BulkUpsert(
		t.Context(),
		"lead",
		map[string]any{"email": "a@x", "name": "Alice"},
		[]string{"email"},
		WithMode("insert_if_missing"),
	)
	if err != nil {
		t.Fatalf("BulkUpsert: %v", err)
	}

	if cap.path != "/api/actions/lead.__upsert" {
		t.Errorf("path: got %q, want /api/actions/lead.__upsert", cap.path)
	}
	if got, _ := cap.body["entity"].(string); got != "lead" {
		t.Errorf("body.entity: got %q", got)
	}
	if got, _ := cap.body["mode"].(string); got != "insert_if_missing" {
		t.Errorf("body.mode: got %q, want insert_if_missing", got)
	}
	uniqueBy, ok := cap.body["unique_by"].([]any)
	if !ok || len(uniqueBy) != 1 || uniqueBy[0] != "email" {
		t.Errorf("body.unique_by: got %+v, want [email]", cap.body["unique_by"])
	}
	if _, ok := cap.body["object_ids"]; ok {
		t.Errorf("__upsert must not carry object_ids: got %+v", cap.body)
	}
	assertFlatNoLegacy(t, cap.body)

	if len(res.Rows) != 1 || res.Rows[0].PK != "u1" || res.Rows[0].Action != "inserted" {
		t.Errorf("rows: got %+v", res.Rows)
	}
	if res.Rows[0].Row["email"] != "a@x" {
		t.Errorf("row payload: got %+v", res.Rows[0].Row)
	}
}

// TestBulkUpsert_omits_mode_when_unset proves the wire stays minimal: an
// unset WithMode option does NOT emit `mode: ""` (server would reject "").
func TestBulkUpsert_omits_mode_when_unset(t *testing.T) {
	srv, cap := newCaptureServer(t, `{"status":"success","result":{"rows":[]}}`)
	c := New(Config{BaseURL: srv.URL})
	if _, err := c.Data().BulkUpsert(t.Context(), "lead",
		map[string]any{"email": "a@x"},
		[]string{"email"},
	); err != nil {
		t.Fatalf("BulkUpsert: %v", err)
	}
	if _, ok := cap.body["mode"]; ok {
		t.Errorf("body.mode must be absent when WithMode not used; got %+v", cap.body)
	}
}

// TestBulkUpsert_rejects_empty_inputs guards programmer errors.
func TestBulkUpsert_rejects_empty_inputs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("server must not be hit on empty-input rejection")
	}))
	t.Cleanup(srv.Close)
	c := New(Config{BaseURL: srv.URL})
	if _, err := c.Data().BulkUpsert(t.Context(), "", map[string]any{"x": 1}, []string{"x"}); err == nil {
		t.Error("empty entity: want error")
	}
	if _, err := c.Data().BulkUpsert(t.Context(), "lead", nil, []string{"x"}); err == nil {
		t.Error("empty fields: want error")
	}
	if _, err := c.Data().BulkUpsert(t.Context(), "lead", map[string]any{"x": 1}, nil); err == nil {
		t.Error("empty unique_by: want error")
	}
}

// TestBulkUpdate_flat_envelope pins:
//   - POST /api/actions/{entity}.__update
//   - body = {"object_ids", "entity", "fields", "condition"?, "error_if_not_found"?}
//   - no `params:` wrapper, no legacy `items[]`
//   - response envelope {status, result: {rows, rows_matched}, audit_operation_id}
//     unwrapped to BulkUpdateResult.
func TestBulkUpdate_flat_envelope(t *testing.T) {
	srv, cap := newCaptureServer(t, `{
		"status": "success",
		"result": {
			"rows": [
				{"id": "u1", "name": "Alice", "$row_version": 2},
				{"id": "u2", "name": "Alice", "$row_version": 2}
			],
			"rows_matched": 2
		},
		"audit_operation_id": "op-upd"
	}`)

	c := New(Config{BaseURL: srv.URL})
	res, err := c.Data().BulkUpdate(
		t.Context(),
		"lead",
		[]string{"u1", "u2"},
		map[string]any{"name": "Alice"},
		WithCondition("entity.status == 'pending'"),
		WithErrorIfNotFound(true),
	)
	if err != nil {
		t.Fatalf("BulkUpdate: %v", err)
	}

	if cap.path != "/api/actions/lead.__update" {
		t.Errorf("path: got %q, want /api/actions/lead.__update", cap.path)
	}
	ids, ok := cap.body["object_ids"].([]any)
	if !ok || len(ids) != 2 || ids[0] != "u1" || ids[1] != "u2" {
		t.Errorf("body.object_ids: got %+v", cap.body["object_ids"])
	}
	if got, _ := cap.body["entity"].(string); got != "lead" {
		t.Errorf("body.entity: got %q", got)
	}
	fields, ok := cap.body["fields"].(map[string]any)
	if !ok || fields["name"] != "Alice" {
		t.Errorf("body.fields: got %+v", cap.body["fields"])
	}
	if cap.body["condition"] != "entity.status == 'pending'" {
		t.Errorf("body.condition: got %v", cap.body["condition"])
	}
	if cap.body["error_if_not_found"] != true {
		t.Errorf("body.error_if_not_found: got %v, want true", cap.body["error_if_not_found"])
	}
	assertFlatNoLegacy(t, cap.body)

	if res.RowsMatched != 2 {
		t.Errorf("rows_matched: got %d, want 2", res.RowsMatched)
	}
	if len(res.Rows) != 2 {
		t.Fatalf("rows: got %d, want 2", len(res.Rows))
	}
	if res.AuditOperationID != "op-upd" {
		t.Errorf("audit_operation_id: got %q", res.AuditOperationID)
	}
}

// TestBulkUpdate_omits_optional_fields proves zero-value options do not
// emit `condition: ""` or `error_if_not_found: false` on the wire.
// `condition: ""` would override a YAML-level default to empty server-side;
// `error_if_not_found: false` is the platform default and adding it just
// noises the wire.
func TestBulkUpdate_omits_optional_fields(t *testing.T) {
	srv, cap := newCaptureServer(t, `{"status":"success","result":{"rows":[],"rows_matched":0}}`)
	c := New(Config{BaseURL: srv.URL})
	if _, err := c.Data().BulkUpdate(t.Context(), "lead",
		[]string{"u1"},
		map[string]any{"name": "x"},
	); err != nil {
		t.Fatalf("BulkUpdate: %v", err)
	}
	if _, ok := cap.body["condition"]; ok {
		t.Errorf("condition must be absent: got %+v", cap.body)
	}
	if _, ok := cap.body["error_if_not_found"]; ok {
		t.Errorf("error_if_not_found must be absent when false: got %+v", cap.body)
	}
}

// TestBulkUpdate_propagates_http_error surfaces non-2xx from the action
// endpoint as APIError. Callers errors.As on it.
func TestBulkUpdate_propagates_http_error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(422)
		_, _ = w.Write([]byte(`{"error":{"code":"VALIDATION","message":"missing pk"}}`))
	}))
	t.Cleanup(srv.Close)

	c := New(Config{BaseURL: srv.URL})
	_, err := c.Data().BulkUpdate(t.Context(), "lead", []string{"u1"}, map[string]any{"name": "x"})
	if err == nil {
		t.Fatal("BulkUpdate: want error on 422")
	}
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("err type: got %T, want *APIError", err)
	}
	if apiErr.StatusCode != 422 {
		t.Errorf("status: got %d, want 422", apiErr.StatusCode)
	}
	if !strings.Contains(apiErr.Body, "VALIDATION") {
		t.Errorf("body not preserved: %q", apiErr.Body)
	}
	if apiErr.Path != "/api/actions/lead.__update" {
		t.Errorf("path: got %q", apiErr.Path)
	}
}

// TestBulkUpdate_rejects_empty_inputs guards programmer errors.
func TestBulkUpdate_rejects_empty_inputs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("server must not be hit on empty-input rejection")
	}))
	t.Cleanup(srv.Close)
	c := New(Config{BaseURL: srv.URL})
	if _, err := c.Data().BulkUpdate(t.Context(), "", []string{"u1"}, map[string]any{"x": 1}); err == nil {
		t.Error("empty entity: want error")
	}
	if _, err := c.Data().BulkUpdate(t.Context(), "lead", nil, map[string]any{"x": 1}); err == nil {
		t.Error("empty object_ids: want error")
	}
	if _, err := c.Data().BulkUpdate(t.Context(), "lead", []string{"u1"}, nil); err == nil {
		t.Error("empty fields: want error")
	}
}

// TestBulkDelete_flat_envelope pins:
//   - POST /api/actions/{entity}.__delete
//   - body = {"object_ids", "entity"} — NO legacy `_ids`
//   - response {status, result: {deleted}, audit_operation_id} unwrapped.
func TestBulkDelete_flat_envelope(t *testing.T) {
	srv, cap := newCaptureServer(t, `{
		"status": "success",
		"result": {"deleted": 2},
		"audit_operation_id": "op-del"
	}`)

	c := New(Config{BaseURL: srv.URL})
	res, err := c.Data().BulkDelete(t.Context(), "lead", []string{"u1", "u2"})
	if err != nil {
		t.Fatalf("BulkDelete: %v", err)
	}

	if cap.path != "/api/actions/lead.__delete" {
		t.Errorf("path: got %q, want /api/actions/lead.__delete", cap.path)
	}
	ids, ok := cap.body["object_ids"].([]any)
	if !ok || len(ids) != 2 || ids[0] != "u1" || ids[1] != "u2" {
		t.Errorf("body.object_ids: got %+v", cap.body["object_ids"])
	}
	if got, _ := cap.body["entity"].(string); got != "lead" {
		t.Errorf("body.entity: got %q", got)
	}
	assertFlatNoLegacy(t, cap.body)

	if res.Deleted != 2 {
		t.Errorf("deleted: got %d, want 2", res.Deleted)
	}
	if res.AuditOperationID != "op-del" {
		t.Errorf("audit_operation_id: got %q", res.AuditOperationID)
	}
}

// TestBulkDelete_rejects_empty_inputs guards programmer errors. Mirrors the
// server's rule exactly: a delete that addresses NOTHING - no ids and no
// predicate - must never leave the client, because on the wire it would read
// like a request to delete everything.
func TestBulkDelete_rejects_empty_inputs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("server must not be hit on empty-input rejection")
	}))
	t.Cleanup(srv.Close)
	c := New(Config{BaseURL: srv.URL})
	if _, err := c.Data().BulkDelete(t.Context(), "", []string{"u1"}); err == nil {
		t.Error("empty entity: want error")
	}
	if _, err := c.Data().BulkDelete(t.Context(), "lead", nil); err == nil {
		t.Error("no ids and no filters: want error")
	}
	if _, err := c.Data().BulkDelete(t.Context(), "lead", nil, DeleteWhere()); err == nil {
		t.Error("no ids and an empty filter list: want error")
	}
}

// TestBulkDelete_where_sends_filters pins the predicate-addressed wire shape:
// `filters` alongside an ABSENT object_ids (not an empty array, which the
// server would have to distinguish from "no ids at all").
func TestBulkDelete_where_sends_filters(t *testing.T) {
	srv, cap := newCaptureServer(t, `{"status":"success","result":{"deleted":7}}`)

	c := New(Config{BaseURL: srv.URL})
	res, err := c.Data().BulkDelete(t.Context(), "arm_result", nil,
		DeleteWhere(Eq("backtest_id", "b1"), Eq("execution_origin", "backtest")))
	if err != nil {
		t.Fatalf("BulkDelete: %v", err)
	}
	if cap.path != "/api/actions/arm_result.__delete" {
		t.Errorf("path: got %q", cap.path)
	}
	if _, present := cap.body["object_ids"]; present {
		t.Error("body.object_ids must be absent when addressing by predicate alone")
	}
	filters, ok := cap.body["filters"].([]any)
	if !ok || len(filters) != 2 {
		t.Fatalf("body.filters: got %+v, want 2 nodes", cap.body["filters"])
	}
	first, _ := filters[0].(map[string]any)
	if first["field"] != "backtest_id" || first["op"] != "eq" || first["value"] != "b1" {
		t.Errorf("body.filters[0]: got %+v", first)
	}
	if res.Deleted != 7 {
		t.Errorf("deleted: got %d, want 7", res.Deleted)
	}
}

// TestBulkDelete_ids_and_filters_together sends both - the guard form, "delete
// these ids, but only while they still match".
func TestBulkDelete_ids_and_filters_together(t *testing.T) {
	srv, cap := newCaptureServer(t, `{"status":"success","result":{"deleted":1}}`)

	c := New(Config{BaseURL: srv.URL})
	if _, err := c.Data().BulkDelete(t.Context(), "lead", []string{"u1"},
		DeleteWhere(Eq("stage", "won"))); err != nil {
		t.Fatalf("BulkDelete: %v", err)
	}
	ids, ok := cap.body["object_ids"].([]any)
	if !ok || len(ids) != 1 || ids[0] != "u1" {
		t.Errorf("body.object_ids: got %+v", cap.body["object_ids"])
	}
	if _, ok := cap.body["filters"].([]any); !ok {
		t.Errorf("body.filters: got %+v, want the guard predicate", cap.body["filters"])
	}
}

// TestBulkDelete_propagates_http_error surfaces non-2xx as APIError.
func TestBulkDelete_propagates_http_error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		_, _ = w.Write([]byte(`{"error":{"code":"FORBIDDEN","message":"no delete perm"}}`))
	}))
	t.Cleanup(srv.Close)

	c := New(Config{BaseURL: srv.URL})
	_, err := c.Data().BulkDelete(t.Context(), "lead", []string{"u1"})
	if err == nil {
		t.Fatal("BulkDelete: want error on 403")
	}
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("err type: got %T, want *APIError", err)
	}
	if apiErr.StatusCode != 403 {
		t.Errorf("status: got %d, want 403", apiErr.StatusCode)
	}
	if apiErr.Path != "/api/actions/lead.__delete" {
		t.Errorf("path: got %q", apiErr.Path)
	}
}

// TestBulkRestore_flat_envelope pins:
//   - POST /api/actions/{entity}.__restore
//   - body = {"object_ids", "entity"}
//   - response {status, result: {restored}, audit_operation_id} unwrapped.
func TestBulkRestore_flat_envelope(t *testing.T) {
	srv, cap := newCaptureServer(t, `{
		"status": "success",
		"result": {"restored": 1},
		"audit_operation_id": "op-restore"
	}`)

	c := New(Config{BaseURL: srv.URL})
	res, err := c.Data().BulkRestore(t.Context(), "lead", []string{"u1"})
	if err != nil {
		t.Fatalf("BulkRestore: %v", err)
	}

	if cap.path != "/api/actions/lead.__restore" {
		t.Errorf("path: got %q, want /api/actions/lead.__restore", cap.path)
	}
	ids, ok := cap.body["object_ids"].([]any)
	if !ok || len(ids) != 1 || ids[0] != "u1" {
		t.Errorf("body.object_ids: got %+v", cap.body["object_ids"])
	}
	if got, _ := cap.body["entity"].(string); got != "lead" {
		t.Errorf("body.entity: got %q", got)
	}
	assertFlatNoLegacy(t, cap.body)

	if res.Restored != 1 {
		t.Errorf("restored: got %d, want 1", res.Restored)
	}
	if res.AuditOperationID != "op-restore" {
		t.Errorf("audit_operation_id: got %q", res.AuditOperationID)
	}
}

// TestBulkRestore_rejects_empty_inputs guards programmer errors.
func TestBulkRestore_rejects_empty_inputs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("server must not be hit on empty-input rejection")
	}))
	t.Cleanup(srv.Close)
	c := New(Config{BaseURL: srv.URL})
	if _, err := c.Data().BulkRestore(t.Context(), "", []string{"u1"}); err == nil {
		t.Error("empty entity: want error")
	}
	if _, err := c.Data().BulkRestore(t.Context(), "lead", nil); err == nil {
		t.Error("empty object_ids: want error")
	}
}

// TestFilterNode_json_shape pins the wire format: tags, omitempty, nested Or/And.
// Prevents accidental renames or tag drift that would silently break server parsing.
func TestFilterNode_json_shape(t *testing.T) {
	cases := []struct {
		name string
		node FilterNode
		want string
	}{
		{"eq_leaf", Eq("name", "alice"), `{"field":"name","op":"eq","value":"alice"}`},
		{"is_empty_no_value", IsEmpty("company_id"), `{"field":"company_id","op":"is_empty"}`},
		{"in_array", In("status", "new", "open"), `{"field":"status","op":"in","value":["new","open"]}`},
		{"or_group", FilterNode{Or: [][]FilterNode{{Eq("a", 1)}, {Eq("b", 2)}}},
			`{"or":[[{"field":"a","op":"eq","value":1}],[{"field":"b","op":"eq","value":2}]]}`},
		{"and_group", FilterNode{And: []FilterNode{Eq("a", 1), Eq("b", 2)}},
			`{"and":[{"field":"a","op":"eq","value":1},{"field":"b","op":"eq","value":2}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := json.Marshal(tc.node)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if string(got) != tc.want {
				t.Errorf("\ngot:  %s\nwant: %s", got, tc.want)
			}
		})
	}
}
