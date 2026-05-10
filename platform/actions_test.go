package platform

import (
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
