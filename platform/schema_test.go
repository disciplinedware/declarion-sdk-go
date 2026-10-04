package platform

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/disciplinedware/declarion-sdk-go/errs"
)

func TestEntitySchemaUsesNativeAuthenticatedTenantTransport(t *testing.T) {
	wantSchema := map[string]any{
		"code": "document",
		"fields": map[string]any{
			"event_data": map[string]any{"source": "$row.event_data.gateway"},
		},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/schema/entities/document" {
			t.Errorf("request = %s %s", r.Method, r.URL.String())
		}
		if got := r.Header.Get("Authorization"); got != "Bearer schema-token" {
			t.Errorf("authorization = %q", got)
		}
		if got := r.Header.Get(TargetTenantIDHeader); got != "tenant-target" {
			t.Errorf("target tenant = %q", got)
		}
		if got := r.Header.Get(ForwardedForHeader); got != "192.0.2.9" {
			t.Errorf("forwarded client = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": wantSchema})
	}))
	defer server.Close()

	client := New(Config{BaseURL: server.URL, Token: "schema-token", TargetTenantID: "tenant-default", HTTPClient: server.Client()})
	ctx := WithOriginatingClientIP(context.Background(), "192.0.2.9:43123")
	schema, err := client.EntitySchema(ctx, "document", WithTargetTenantID("tenant-target"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(schema, wantSchema) {
		t.Fatalf("entity schema = %#v, want %#v", schema, wantSchema)
	}
}

func TestEntitySchemaPreservesStructuredAuthorizationErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", ProblemContentType)
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"type":"/errors/permission.denied","status":403,"title":"Denied","detail":"schema read refused"}`))
	}))
	defer server.Close()

	client := New(Config{BaseURL: server.URL, Token: "schema-token", HTTPClient: server.Client()})
	_, err := client.EntitySchema(t.Context(), "document")
	apiError, ok := errs.From(err)
	if !ok || apiError.Code() != "permission.denied" || apiError.Status != http.StatusForbidden {
		t.Fatalf("schema error = %#v, parsed=%v", err, ok)
	}
}

func TestEntitySchemaRejectsMissingDataEnvelope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"entity":{"fields":{}}}`))
	}))
	defer server.Close()

	client := New(Config{BaseURL: server.URL, Token: "schema-token", HTTPClient: server.Client()})
	if _, err := client.EntitySchema(t.Context(), "document"); err == nil {
		t.Fatal("response without native data envelope was accepted")
	}
}
