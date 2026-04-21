package platform

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestBulkUpsert_was_inserted_unmarshaling(t *testing.T) {
	cases := []struct {
		name        string
		serverBody  string
		wantInserted []bool
	}{
		{
			name: "single_insert",
			serverBody: `[{"id":"abc","name":"Alice","was_inserted":true}]`,
			wantInserted: []bool{true},
		},
		{
			name: "single_update",
			serverBody: `[{"id":"abc","name":"Alice","was_inserted":false}]`,
			wantInserted: []bool{false},
		},
		{
			name: "was_inserted_absent_defaults_false",
			serverBody: `[{"id":"abc","name":"Alice"}]`,
			wantInserted: []bool{false},
		},
		{
			name: "mixed_batch",
			serverBody: `[{"id":"a","was_inserted":true},{"id":"b","was_inserted":false},{"id":"c"}]`,
			wantInserted: []bool{true, false, false},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.serverBody))
			}))
			t.Cleanup(srv.Close)

			c := New(Config{BaseURL: srv.URL})
			items, err := c.Data().BulkUpsert(t.Context(), "thing", "id", []map[string]any{{"id": "x"}})
			if err != nil {
				t.Fatalf("BulkUpsert: %v", err)
			}

			if len(items) != len(tc.wantInserted) {
				t.Fatalf("want %d items, got %d", len(tc.wantInserted), len(items))
			}
			for i, want := range tc.wantInserted {
				if items[i].WasInserted != want {
					t.Errorf("items[%d].WasInserted: got %v, want %v", i, items[i].WasInserted, want)
				}
				// was_inserted must not leak into Fields.
				if _, ok := items[i].Fields["was_inserted"]; ok {
					t.Errorf("items[%d].Fields must not contain was_inserted", i)
				}
			}
		})
	}
}

func TestBulkUpsert_fields_accessible(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body, _ := json.Marshal([]map[string]any{
			{"id": "uuid-1", "name": "Alice", "was_inserted": true},
		})
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)

	c := New(Config{BaseURL: srv.URL})
	items, err := c.Data().BulkUpsert(t.Context(), "lead", "id", []map[string]any{{"id": "uuid-1"}})
	if err != nil {
		t.Fatalf("BulkUpsert: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("want 1 item, got %d", len(items))
	}

	item := items[0]
	if !item.WasInserted {
		t.Error("WasInserted must be true")
	}
	if item.Fields["id"] != "uuid-1" {
		t.Errorf("Fields[id]: got %v, want uuid-1", item.Fields["id"])
	}
	if item.Fields["name"] != "Alice" {
		t.Errorf("Fields[name]: got %v, want Alice", item.Fields["name"])
	}
}

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
		IncludeCount:   true,
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
		"include_count":   "true",
		"include_deleted": "true",
	}
	for k, v := range want {
		if got := captured.Get(k); got != v {
			t.Errorf("query[%s]: got %q, want %q", k, got, v)
		}
	}

	// filters must be valid JSON matching our tree.
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
	// Absent-when-empty: bare params (no filters, no select, etc.) must not emit stale keys.
	// Verified separately in TestList_omits_empty_params.
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

	forbidden := []string{"limit", "after", "page", "per_page", "sort", "search", "filters", "select", "include_count", "include_deleted"}
	for _, k := range forbidden {
		if _, ok := captured[k]; ok {
			t.Errorf("empty ListParams must not emit %q (got %q)", k, captured.Get(k))
		}
	}
}

// TestList_offset_mode_params — page/per_page emit correctly and coexist
// with sort/search. The SDK does not arbitrate cursor vs offset; the server's
// pagination-mode selection logic decides (Limit wins if both set).
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
