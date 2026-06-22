package platform

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// Get must address a row by the entity's REAL primary key - any single field
// name (id OR code OR ...) or a composite key - by emitting each pk map entry as
// its own query param, never a hardcoded `id`. (Parity with the React SDK fix:
// a `?id=` hardcode 400s for a non-id-PK entity such as schedule.code.)
func TestGet_addresses_by_real_pk_fields(t *testing.T) {
	cases := []struct {
		name string
		pk   map[string]any
		want map[string]string
	}{
		{"single_id_pk", map[string]any{"id": "u1"}, map[string]string{"id": "u1"}},
		{"single_non_id_pk_code", map[string]any{"code": "files.gc_sweep"}, map[string]string{"code": "files.gc_sweep"}},
		{"composite_pk", map[string]any{"tenant_id": "t1", "code": "acme"}, map[string]string{"tenant_id": "t1", "code": "acme"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotQuery url.Values
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotQuery = r.URL.Query()
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"data":{"ok":true}}`))
			}))
			t.Cleanup(srv.Close)

			c := New(Config{BaseURL: srv.URL})
			if _, err := c.Data().Get(t.Context(), "thing", tc.pk); err != nil {
				t.Fatalf("Get: %v", err)
			}
			for k, want := range tc.want {
				if got := gotQuery.Get(k); got != want {
					t.Errorf("query %q = %q, want %q", k, got, want)
				}
			}
			// A non-id PK must NOT silently emit ?id=.
			if _, hasID := tc.want["id"]; !hasID && gotQuery.Has("id") {
				t.Errorf("must not emit ?id= for a non-id PK; query = %v", gotQuery)
			}
		})
	}
}

func TestGet_empty_pk_errors(t *testing.T) {
	c := New(Config{BaseURL: "http://unused.invalid"})
	if _, err := c.Data().Get(t.Context(), "thing", nil); err == nil {
		t.Fatal("expected an error for an empty pk map")
	}
}

// A composite primary key is encoded by the CALLER as a single object_id string
// joining the PK fields by U+001F (matching store.SplitObjectID server-side).
// The SDK must pass that object_id through verbatim - not re-split or mangle it.
func TestBulkUpdate_passes_composite_object_id_verbatim(t *testing.T) {
	const compositeID = "t1\x1facme" // tenant_id=t1 U+001F code=acme

	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","result":{"rows":[],"rows_matched":1}}`))
	}))
	t.Cleanup(srv.Close)

	c := New(Config{BaseURL: srv.URL})
	if _, err := c.Data().BulkUpdate(t.Context(), "commerce_billing_account",
		[]string{compositeID}, map[string]any{"name": "Acme"}); err != nil {
		t.Fatalf("BulkUpdate: %v", err)
	}

	ids, ok := gotBody["object_ids"].([]any)
	if !ok || len(ids) != 1 {
		t.Fatalf("object_ids = %v, want one element", gotBody["object_ids"])
	}
	if ids[0] != compositeID {
		t.Errorf("object_id mangled: got %q, want %q (the U+001F-joined composite key)", ids[0], compositeID)
	}
}
