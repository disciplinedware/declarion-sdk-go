package platform

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestBatch_typed_helpers_build_flat_envelopes pins the FLAT body each
// Batch.{Create,Upsert,Update,Delete,Restore} op carries. The system.batch
// dispatcher applies these maps verbatim as the FLAT action body for the
// target endpoint — drift here silently breaks server-side parsing on
// every wrapped op.
func TestBatch_typed_helpers_build_flat_envelopes(t *testing.T) {
	b := (&Client{}).NewBatch().
		Create("lead", map[string]any{"name": "Alice"}).
		Upsert("lead", map[string]any{"email": "a@x"}, []string{"email"}, "insert_if_missing").
		Update("lead", []string{"u1"}, map[string]any{"name": "Bob"},
			WithCondition("entity.status == 'pending'"),
			WithErrorIfNotFound(true),
		).
		Delete("lead", []string{"u1", "u2"}).
		Restore("lead", []string{"u3"})

	if len(b.ops) != 5 {
		t.Fatalf("ops: got %d, want 5", len(b.ops))
	}

	// Op 0: __create — entity + fields only, no object_ids.
	if b.ops[0].Action != "lead.__create" {
		t.Errorf("ops[0].action: got %q", b.ops[0].Action)
	}
	if _, ok := b.ops[0].Params["object_ids"]; ok {
		t.Errorf("__create must not carry object_ids: %+v", b.ops[0].Params)
	}
	if b.ops[0].Params["entity"] != "lead" || b.ops[0].Params["fields"].(map[string]any)["name"] != "Alice" {
		t.Errorf("ops[0].params: got %+v", b.ops[0].Params)
	}
	assertOpFlat(t, b.ops[0])

	// Op 1: __upsert — entity + fields + unique_by + mode, no object_ids.
	if b.ops[1].Action != "lead.__upsert" {
		t.Errorf("ops[1].action: got %q", b.ops[1].Action)
	}
	if _, ok := b.ops[1].Params["object_ids"]; ok {
		t.Errorf("__upsert must not carry object_ids: %+v", b.ops[1].Params)
	}
	if b.ops[1].Params["mode"] != "insert_if_missing" {
		t.Errorf("ops[1].mode: got %v", b.ops[1].Params["mode"])
	}
	uniqueBy, ok := b.ops[1].Params["unique_by"].([]string)
	if !ok || len(uniqueBy) != 1 || uniqueBy[0] != "email" {
		t.Errorf("ops[1].unique_by: got %+v", b.ops[1].Params["unique_by"])
	}
	assertOpFlat(t, b.ops[1])

	// Op 2: __update — object_ids + entity + fields + condition + error_if_not_found.
	if b.ops[2].Action != "lead.__update" {
		t.Errorf("ops[2].action: got %q", b.ops[2].Action)
	}
	ids, ok := b.ops[2].Params["object_ids"].([]string)
	if !ok || len(ids) != 1 || ids[0] != "u1" {
		t.Errorf("ops[2].object_ids: got %+v", b.ops[2].Params["object_ids"])
	}
	if b.ops[2].Params["condition"] != "entity.status == 'pending'" {
		t.Errorf("ops[2].condition: got %v", b.ops[2].Params["condition"])
	}
	if b.ops[2].Params["error_if_not_found"] != true {
		t.Errorf("ops[2].error_if_not_found: got %v", b.ops[2].Params["error_if_not_found"])
	}
	assertOpFlat(t, b.ops[2])

	// Op 3: __delete — object_ids + entity only.
	if b.ops[3].Action != "lead.__delete" {
		t.Errorf("ops[3].action: got %q", b.ops[3].Action)
	}
	if _, ok := b.ops[3].Params["fields"]; ok {
		t.Errorf("__delete must not carry fields: %+v", b.ops[3].Params)
	}
	delIDs, ok := b.ops[3].Params["object_ids"].([]string)
	if !ok || len(delIDs) != 2 {
		t.Errorf("ops[3].object_ids: got %+v", b.ops[3].Params["object_ids"])
	}
	assertOpFlat(t, b.ops[3])

	// Op 4: __restore — object_ids + entity only.
	if b.ops[4].Action != "lead.__restore" {
		t.Errorf("ops[4].action: got %q", b.ops[4].Action)
	}
	if _, ok := b.ops[4].Params["fields"]; ok {
		t.Errorf("__restore must not carry fields: %+v", b.ops[4].Params)
	}
	assertOpFlat(t, b.ops[4])
}

// assertOpFlat fails the test if a batch op's params carry any of the
// legacy wrapper keys the 2026-06-01 envelope explicitly forbids.
func assertOpFlat(t *testing.T, op BatchOp) {
	t.Helper()
	for _, k := range []string{"_ids", "items", "records", "params"} {
		if _, ok := op.Params[k]; ok {
			t.Errorf("op %q params must not carry %q (got %+v)", op.Action, k, op.Params)
		}
	}
}

// TestBatch_Update_omits_optional_fields proves zero-value UpdateOptions
// do not emit `condition: ""` / `error_if_not_found: false` on the wire.
func TestBatch_Update_omits_optional_fields(t *testing.T) {
	b := (&Client{}).NewBatch().Update("lead", []string{"u1"}, map[string]any{"name": "x"})
	if len(b.ops) != 1 {
		t.Fatalf("ops: got %d", len(b.ops))
	}
	if _, ok := b.ops[0].Params["condition"]; ok {
		t.Errorf("condition must be absent when not set: %+v", b.ops[0].Params)
	}
	if _, ok := b.ops[0].Params["error_if_not_found"]; ok {
		t.Errorf("error_if_not_found must be absent when false: %+v", b.ops[0].Params)
	}
}

// TestBatch_Upsert_omits_mode_when_blank — empty mode stays off the wire.
func TestBatch_Upsert_omits_mode_when_blank(t *testing.T) {
	b := (&Client{}).NewBatch().Upsert("lead", map[string]any{"x": 1}, []string{"x"}, "")
	if _, ok := b.ops[0].Params["mode"]; ok {
		t.Errorf("mode must be absent when blank: %+v", b.ops[0].Params)
	}
}

// TestBatch_BuilderCallAndCreate covers the generic .Call path for non-CRUD
// actions composed alongside data.* ops.
func TestBatch_BuilderCallAndCreate(t *testing.T) {
	b := (&Client{}).NewBatch().
		Call("myapp.actions.http_request", map[string]any{"url": "https://example.com"}).
		Create("state_counters", map[string]any{"code": "x", "count": 1})

	if len(b.ops) != 2 {
		t.Fatalf("ops: got %d, want 2", len(b.ops))
	}
	if b.ops[0].Action != "myapp.actions.http_request" {
		t.Errorf("op0 action: got %q", b.ops[0].Action)
	}
	if b.ops[1].Action != "state_counters.__create" {
		t.Errorf("op1 action: got %q", b.ops[1].Action)
	}
	if b.ops[1].Params["entity"] != "state_counters" {
		t.Errorf("op1 entity: got %v", b.ops[1].Params["entity"])
	}
}

func TestBatch_ExecuteEmpty(t *testing.T) {
	c := New(Config{BaseURL: "http://unused.local"})
	if _, err := c.NewBatch().Execute(t.Context()); err == nil {
		t.Fatal("Execute with no ops: want error, got nil")
	}
}

func TestBatch_ExecuteRoundtrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/actions/system.batch" {
			t.Errorf("path: got %q", r.URL.Path)
		}
		// Decode body to verify wire shape.
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body["atomic"] != true {
			t.Errorf("atomic: got %v", body["atomic"])
		}
		actions, ok := body["actions"].([]any)
		if !ok || len(actions) != 1 {
			t.Fatalf("actions: got %v", body["actions"])
		}
		// Echo a committed=true response with one ok result.
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"status": "success",
			"result": {
				"committed": true,
				"results": [{"index": 0, "ok": true, "result": {"job_id": "job-123"}}]
			}
		}`))
	}))
	t.Cleanup(srv.Close)

	c := New(Config{BaseURL: srv.URL})
	resp, err := c.NewBatch().
		Call("myapp.actions.http_request", map[string]any{"url": "https://example.com"}).
		Execute(t.Context())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !resp.Committed {
		t.Fatal("expected committed=true")
	}
	if len(resp.Results) != 1 || !resp.Results[0].OK {
		t.Fatalf("results: %+v", resp.Results)
	}
	m, ok := resp.Results[0].Result.(map[string]any)
	if !ok || m["job_id"] != "job-123" {
		t.Errorf("expected job_id=job-123, got %v", resp.Results[0].Result)
	}
}

func TestBatch_ExecuteSendsTargetTenantHeader(t *testing.T) {
	targetTenantID := "11111111-1111-1111-1111-111111111111"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get(TargetTenantIDHeader); got != targetTenantID {
			t.Errorf("%s = %q, want %q", TargetTenantIDHeader, got, targetTenantID)
		}
		if got := r.Header.Get(TargetTenantCodeHeader); got != "" {
			t.Errorf("%s = %q, want empty", TargetTenantCodeHeader, got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","result":{"committed":true,"results":[{"index":0,"ok":true}]}}`))
	}))
	t.Cleanup(srv.Close)

	c := New(Config{BaseURL: srv.URL})
	_, err := c.NewBatch().
		WithTargetTenantID(targetTenantID).
		Call("a.b", nil).
		Execute(t.Context())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
}

func TestBatch_ExecuteSurfacesLogicalFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"status": "success",
			"result": {
				"committed": false,
				"results": [
					{"index": 0, "ok": true, "result": {}},
					{"index": 1, "ok": false, "error": "permission denied", "error_code": "PERMISSION_DENIED"}
				]
			}
		}`))
	}))
	t.Cleanup(srv.Close)

	c := New(Config{BaseURL: srv.URL})
	resp, err := c.NewBatch().
		Call("a.b", nil).
		Call("c.d", nil).
		Execute(t.Context())
	if err != nil {
		t.Fatalf("Execute returned Go error on logical failure (should not): %v", err)
	}
	if resp.Committed {
		t.Fatal("expected committed=false")
	}
	if resp.Results[1].ErrorCode != "PERMISSION_DENIED" {
		t.Errorf("error_code: got %q", resp.Results[1].ErrorCode)
	}
}

func TestBatch_ExecuteHTTPErrorReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	}))
	t.Cleanup(srv.Close)

	c := New(Config{BaseURL: srv.URL})
	_, err := c.NewBatch().Call("x.y", nil).Execute(t.Context())
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("expected 5xx APIError, got %v", err)
	}
}
