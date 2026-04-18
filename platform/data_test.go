package platform

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
