package platform

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestActionsInvokeSendsTargetTenantCodeHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/actions/test.action" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get(TargetTenantCodeHeader); got != "default" {
			t.Errorf("%s = %q, want default", TargetTenantCodeHeader, got)
		}
		if got := r.Header.Get(TargetTenantIDHeader); got != "" {
			t.Errorf("%s = %q, want empty", TargetTenantIDHeader, got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","result":{"ok":true}}`))
	}))
	t.Cleanup(srv.Close)

	c := New(Config{BaseURL: srv.URL})
	_, err := c.Actions().Invoke(t.Context(), "test.action", InvokeParams{
		Args:             map[string]any{"x": "y"},
		TargetTenantCode: "default",
	})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
}

// TestActionsInvoke_promotes_ids_to_object_ids pins the rename from the
// retired `_ids` reserved key to `object_ids`. parseActionBody in
// declarion-core rejects `_ids` outright (400) as of the 2026-06-01
// write-API rewrite; a regression here breaks every single/batch-scope
// action this SDK dispatches.
func TestActionsInvoke_promotes_ids_to_object_ids(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success"}`))
	}))
	t.Cleanup(srv.Close)

	c := New(Config{BaseURL: srv.URL})
	_, err := c.Actions().Invoke(t.Context(), "lead.archive", InvokeParams{
		Args: map[string]any{"reason": "duplicate"},
		IDs:  []string{"u1", "u2"},
	})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}

	ids, ok := body["object_ids"].([]any)
	if !ok || len(ids) != 2 || ids[0] != "u1" || ids[1] != "u2" {
		t.Errorf("body.object_ids: got %+v", body["object_ids"])
	}
	if _, ok := body["_ids"]; ok {
		t.Errorf("body must not carry legacy `_ids` (server rejects 400): got %+v", body)
	}
	if body["reason"] != "duplicate" {
		t.Errorf("body.reason: got %v, want duplicate", body["reason"])
	}
}
