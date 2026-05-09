package platform

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBatch_BuilderCallAndUpsert(t *testing.T) {
	b := (&Client{}).NewBatch().
		Call("swiftward.actions.http_request", map[string]any{"url": "https://example.com"}).
		Upsert("state_counters", []map[string]any{{"code": "x", "count": 1}}, "upsert", []string{"code"})

	if len(b.ops) != 2 {
		t.Fatalf("ops: got %d, want 2", len(b.ops))
	}
	if b.ops[0].Action != "swiftward.actions.http_request" {
		t.Errorf("op0 action: got %q", b.ops[0].Action)
	}
	if b.ops[1].Action != "data.upsert" {
		t.Errorf("op1 action: got %q", b.ops[1].Action)
	}
	if b.ops[1].Params["entity"] != "state_counters" {
		t.Errorf("op1 entity: got %v", b.ops[1].Params["entity"])
	}
	if b.ops[1].Params["mode"] != "upsert" {
		t.Errorf("op1 mode: got %v", b.ops[1].Params["mode"])
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
		Call("swiftward.actions.http_request", map[string]any{"url": "https://example.com"}).
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
