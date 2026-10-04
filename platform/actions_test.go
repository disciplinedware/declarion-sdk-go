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

	c := New(Config{BaseURL: srv.URL, HTTPClient: srv.Client()})
	_, err := c.Actions().Invoke(t.Context(), "test.action", InvokeParams{
		Args:             map[string]any{"x": "y"},
		TargetTenantCode: "default",
	})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
}

func TestActionsInvokeSendsObjectIDsAndFlatArguments(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success"}`))
	}))
	t.Cleanup(srv.Close)

	c := New(Config{BaseURL: srv.URL, HTTPClient: srv.Client()})
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
	if len(body) != 2 {
		t.Errorf("action envelope must contain object_ids and reason: got %+v", body)
	}
	if body["reason"] != "duplicate" {
		t.Errorf("body.reason: got %v, want duplicate", body["reason"])
	}
}

func TestActionsInvokeSendsParamsFileReferenceAsEnvelope(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success"}`))
	}))
	t.Cleanup(srv.Close)
	c := New(Config{BaseURL: srv.URL, HTTPClient: srv.Client()})
	ref := &ArgsReference{EntityCode: "record", FieldCode: "body", Key: "reference-key", SizeBytes: 12, JSONPointer: "/params"}
	_, err := c.Actions().Invoke(t.Context(), "record.run", InvokeParams{
		Args:          map[string]any{"trace_id": "request-1"},
		ParamsFileRef: ref,
	})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if body["trace_id"] != "request-1" {
		t.Fatalf("handler args = %#v", body)
	}
	encoded, err := json.Marshal(body["params_file_ref"])
	if err != nil {
		t.Fatal(err)
	}
	var got ArgsReference
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if got != *ref {
		t.Fatalf("params_file_ref = %#v, want %#v", got, *ref)
	}
}
