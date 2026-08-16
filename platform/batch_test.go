package platform

import (
	"github.com/disciplinedware/declarion-sdk-go/errs"

	"encoding/json"
	"net/http"
	"net/http/httptest"
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

	// Op 2: __update — object_ids at the op TOP LEVEL; params = entity + fields
	// + condition + error_if_not_found (NO object_ids in params).
	if b.ops[2].Action != "lead.__update" {
		t.Errorf("ops[2].action: got %q", b.ops[2].Action)
	}
	if len(b.ops[2].ObjectIDs) != 1 || b.ops[2].ObjectIDs[0] != "u1" {
		t.Errorf("ops[2].ObjectIDs: got %+v, want [u1]", b.ops[2].ObjectIDs)
	}
	if b.ops[2].Params["condition"] != "entity.status == 'pending'" {
		t.Errorf("ops[2].condition: got %v", b.ops[2].Params["condition"])
	}
	if b.ops[2].Params["error_if_not_found"] != true {
		t.Errorf("ops[2].error_if_not_found: got %v", b.ops[2].Params["error_if_not_found"])
	}
	assertOpFlat(t, b.ops[2])

	// Op 3: __delete — object_ids at the op top level; params = entity only.
	if b.ops[3].Action != "lead.__delete" {
		t.Errorf("ops[3].action: got %q", b.ops[3].Action)
	}
	if _, ok := b.ops[3].Params["fields"]; ok {
		t.Errorf("__delete must not carry fields: %+v", b.ops[3].Params)
	}
	if len(b.ops[3].ObjectIDs) != 2 || b.ops[3].ObjectIDs[0] != "u1" || b.ops[3].ObjectIDs[1] != "u2" {
		t.Errorf("ops[3].ObjectIDs: got %+v, want [u1 u2]", b.ops[3].ObjectIDs)
	}
	assertOpFlat(t, b.ops[3])

	// Op 4: __restore — object_ids at the op top level; params = entity only.
	if b.ops[4].Action != "lead.__restore" {
		t.Errorf("ops[4].action: got %q", b.ops[4].Action)
	}
	if _, ok := b.ops[4].Params["fields"]; ok {
		t.Errorf("__restore must not carry fields: %+v", b.ops[4].Params)
	}
	if len(b.ops[4].ObjectIDs) != 1 || b.ops[4].ObjectIDs[0] != "u3" {
		t.Errorf("ops[4].ObjectIDs: got %+v, want [u3]", b.ops[4].ObjectIDs)
	}
	assertOpFlat(t, b.ops[4])
}

// assertOpFlat fails the test if a batch op's params carry any forbidden key.
// `object_ids` is forbidden in params because it is the op's TOP-LEVEL PK
// channel (server reads BatchOp.ObjectIDs, never params.object_ids); the rest
// are legacy wrapper keys the 2026-06-01 envelope dropped.
func assertOpFlat(t *testing.T, op BatchOp) {
	t.Helper()
	for _, k := range []string{"_ids", "items", "records", "params", "object_ids"} {
		if _, ok := op.Params[k]; ok {
			t.Errorf("op %q params must not carry %q (got %+v); object_ids belongs at the op top level", op.Action, k, op.Params)
		}
	}
}

// TestBatch_Update_omits_optional_fields proves zero-value UpdateOptions
// do not emit `condition: ""` / `error_if_not_found: false` on the wire, and
// that object_ids sits at the op top level (never in params).
func TestBatch_Update_omits_optional_fields(t *testing.T) {
	b := (&Client{}).NewBatch().Update("lead", []string{"u1"}, map[string]any{"name": "x"})
	if len(b.ops) != 1 {
		t.Fatalf("ops: got %d", len(b.ops))
	}
	if len(b.ops[0].ObjectIDs) != 1 || b.ops[0].ObjectIDs[0] != "u1" {
		t.Errorf("object_ids must be at the op top level: ObjectIDs=%+v", b.ops[0].ObjectIDs)
	}
	if _, ok := b.ops[0].Params["object_ids"]; ok {
		t.Errorf("object_ids must NOT be in params: %+v", b.ops[0].Params)
	}
	if _, ok := b.ops[0].Params["condition"]; ok {
		t.Errorf("condition must be absent when not set: %+v", b.ops[0].Params)
	}
	if _, ok := b.ops[0].Params["error_if_not_found"]; ok {
		t.Errorf("error_if_not_found must be absent when false: %+v", b.ops[0].Params)
	}
}

// TestBatch_Update_wire_puts_object_ids_top_level is the regression guard for
// the SDK<->server envelope mismatch: a client-side op-shape unit test cannot
// catch it (the op looks fine in memory), so this drives a real Execute against
// an httptest server and asserts the SERIALIZED op carries object_ids at the
// op top level and NOT inside params - exactly where declarion-core's
// system.batch dispatcher reads it (handler/batch_handler.go BatchOp.ObjectIDs).
// Before the fix this arrived as params.object_ids and the server raised
// "requires object_ids".
func TestBatch_Update_wire_puts_object_ids_top_level(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Actions []map[string]any `json:"actions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if len(body.Actions) != 1 {
			t.Fatalf("actions: got %d, want 1", len(body.Actions))
		}
		op := body.Actions[0]
		ids, ok := op["object_ids"].([]any)
		if !ok || len(ids) != 1 || ids[0] != "u1" {
			t.Errorf("wire op.object_ids (top level): got %v", op["object_ids"])
		}
		params, _ := op["params"].(map[string]any)
		if _, present := params["object_ids"]; present {
			t.Errorf("wire op.params must NOT carry object_ids: %v", params)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","result":{"committed":true,"results":[{"index":0,"ok":true}]}}`))
	}))
	t.Cleanup(srv.Close)

	c := New(Config{BaseURL: srv.URL})
	resp, err := c.NewBatch().
		Update("lead", []string{"u1"}, map[string]any{"name": "x"}).
		Execute(t.Context())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !resp.Committed {
		t.Fatal("expected committed=true")
	}
}

// TestBatch_AddOp_passthrough proves a pre-built op (the path swiftward's
// worker uses to ship []BatchOp it assembled itself) reaches the wire verbatim,
// object_ids at the op top level.
func TestBatch_AddOp_passthrough(t *testing.T) {
	b := (&Client{}).NewBatch().AddOp(BatchOp{
		Action:    "data.update",
		ObjectIDs: []string{"x1"},
		Params:    map[string]any{"entity": "lead", "fields": map[string]any{"name": "y"}},
	})
	if len(b.ops) != 1 {
		t.Fatalf("ops: got %d", len(b.ops))
	}
	if b.ops[0].Action != "data.update" || len(b.ops[0].ObjectIDs) != 1 || b.ops[0].ObjectIDs[0] != "x1" {
		t.Errorf("AddOp did not pass through verbatim: %+v", b.ops[0])
	}
	assertOpFlat(t, b.ops[0])
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
					{"index": 1, "action": "c.d", "ok": false, "error": {"type": "/errors/permission.denied", "title": "You do not have permission to do this.", "retryable": false, "required_permission": "action:c.d"}}
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
	failed := resp.Results[1]
	if failed.Error.Code() != "permission.denied" {
		t.Errorf("type: got %q, want permission.denied", failed.Error.Code())
	}
	// A declared member reaches the caller through the same object.
	if got, _ := failed.Error.ExtString("required_permission"); got != "action:c.d" {
		t.Errorf("required_permission: got %q, want action:c.d", got)
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
	// `{"error":"boom"}` is not an error object, so it is not the platform
	// speaking - it takes the client's own transport type, carrying the status
	// that actually answered.
	e, ok := errs.From(err)
	if !ok {
		t.Fatalf("expected a typed failure, got %T: %v", err, err)
	}
	if e.Code() != TypeUnreadableResponse {
		t.Fatalf("type: got %q, want %q", e.Code(), TypeUnreadableResponse)
	}
	if status, _ := StatusOf(e); status != http.StatusInternalServerError {
		t.Fatalf("status: got %d, want 500", status)
	}
}
