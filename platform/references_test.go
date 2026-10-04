package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/disciplinedware/declarion-sdk-go/errs"
	"github.com/disciplinedware/declarion-sdk-go/fileref"
	"github.com/disciplinedware/declarion-sdk-go/tracing"
)

func TestUploadFileReferenceUsesNativeRequest(t *testing.T) {
	content := []byte{0x00, 0xff, 0x80, 'x'}
	const traceParent = "00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01"
	expiresAt := time.Date(2026, time.October, 5, 0, 0, 0, 0, time.UTC)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != referenceUploadPath {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if r.URL.Query().Get("entity_code") != "record" || r.URL.Query().Get("field_code") != "payload" || r.URL.Query().Get("retry_key") != "attempt/1" {
			t.Errorf("query = %s", r.URL.RawQuery)
		}
		if got := r.Header.Get("Content-Type"); got != "application/octet-stream" {
			t.Errorf("Content-Type = %q", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer token" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get(TargetTenantIDHeader); got != "tenant-2" {
			t.Errorf("target tenant = %q", got)
		}
		if got := r.Header.Get(ForwardedForHeader); got != "192.0.2.4" {
			t.Errorf("forwarded client = %q", got)
		}
		if got := r.Header.Get("traceparent"); got != traceParent {
			t.Errorf("traceparent = %q", got)
		}
		if got := r.Header.Get("tracestate"); got != "vendor=value" {
			t.Errorf("tracestate = %q", got)
		}
		if got := r.Header.Get("baggage"); got != "declarion.trace_path=handler" {
			t.Errorf("baggage = %q", got)
		}
		got, err := io.ReadAll(r.Body)
		if err != nil || !bytes.Equal(got, content) {
			t.Errorf("uploaded bytes = %v, %v", got, err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(FileReferenceReceipt{Key: "objects/key", SizeBytes: int64(len(content)), SHA256: "digest", ExpiresAt: &expiresAt})
	}))
	t.Cleanup(srv.Close)
	client := New(Config{BaseURL: srv.URL, Token: "token", TargetTenantID: "tenant-1", HTTPClient: srv.Client()})
	ctx := tracing.WithWork(tracing.Restore(t.Context(), traceParent, "vendor=value"), "request", "handler")
	ctx = WithOriginatingClientIP(ctx, "192.0.2.4")
	receipt, err := client.UploadFileReference(ctx, "record", "payload", content, "attempt/1", WithTargetTenantID("tenant-2"))
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Key != "objects/key" || receipt.SizeBytes != int64(len(content)) || receipt.SHA256 != "digest" || receipt.ExpiresAt == nil || !receipt.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("receipt = %+v", receipt)
	}
}

func TestReadFileReferenceRuntimeIsBounded(t *testing.T) {
	for _, tc := range []struct {
		name      string
		content   []byte
		limit     int64
		want      []byte
		wantFail  bool
		wantLimit bool
	}{
		{name: "within_limit", content: []byte("abc"), limit: 3, want: []byte("abc")},
		{name: "over_limit", content: []byte("abcd"), limit: 3, wantFail: true, wantLimit: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key, err := fileref.NewKey("objects", "00000000-0000-0000-0000-000000000001", time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC), time.Hour, tc.content)
			if err != nil {
				t.Fatal(err)
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != referenceRuntimePath {
					t.Errorf("request = %s %s", r.Method, r.URL.Path)
				}
				if r.URL.Query().Get("entity_code") != "record" || r.URL.Query().Get("field_code") != "payload" || r.URL.Query().Get("key") != key.String() {
					t.Errorf("query = %s", r.URL.RawQuery)
				}
				if got := r.Header.Get(TargetTenantCodeHeader); got != "acme" {
					t.Errorf("target tenant code = %q", got)
				}
				_, _ = w.Write(tc.content)
			}))
			defer srv.Close()
			client := New(Config{BaseURL: srv.URL, HTTPClient: srv.Client()})
			got, err := client.ReadFileReferenceRuntime(t.Context(), "record", "payload", key.String(), tc.limit, WithTargetTenantCode("acme"))
			if tc.wantFail {
				if err == nil || got != nil {
					t.Fatalf("read = %q, %v; want bounded failure", got, err)
				}
				if tc.wantLimit && !errors.Is(err, ErrFileReferenceLimit) {
					t.Fatalf("read error = %v, want caller-limit error", err)
				}
				return
			}
			if err != nil || !bytes.Equal(got, tc.want) {
				t.Fatalf("read = %q, %v", got, err)
			}
		})
	}
}

func TestReadFileReferenceRuntimeRejectsInvalidKeyBeforeRequest(t *testing.T) {
	client := New(Config{})
	content, err := client.ReadFileReferenceRuntime(t.Context(), "record", "payload", "invalid", 100)
	if content != nil || !errors.Is(err, fileref.ErrInvalidKey) {
		t.Fatalf("read = %q, %v; want invalid-key error without content", content, err)
	}
}

func TestReadFileReferenceRuntimeRejectsDigestMismatch(t *testing.T) {
	key, err := fileref.NewKey("objects", "00000000-0000-0000-0000-000000000001", time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC), time.Hour, []byte("expected"))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("different"))
	}))
	defer srv.Close()
	client := New(Config{BaseURL: srv.URL, HTTPClient: srv.Client()})
	content, err := client.ReadFileReferenceRuntime(t.Context(), "record", "payload", key.String(), 100)
	if content != nil || !errors.Is(err, fileref.ErrDigestMismatch) {
		t.Fatalf("read = %q, %v; want digest mismatch and no content", content, err)
	}
}

func TestReadFileReferenceRuntimeRejectsInvalidLimit(t *testing.T) {
	client := New(Config{})
	for _, limit := range []int64{0, -1} {
		if _, err := client.ReadFileReferenceRuntime(context.Background(), "record", "payload", "key", limit); err == nil {
			t.Errorf("limit %d was accepted", limit)
		}
	}
}

func TestFileReferenceErrorsRemainClassifiable(t *testing.T) {
	key, err := fileref.NewKey("objects", "00000000-0000-0000-0000-000000000001", time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC), time.Hour, []byte("fixture"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name      string
		status    int
		body      string
		wantCode  string
		wantRetry bool
		truncated bool
	}{
		{name: "expired_source", status: http.StatusGone, body: `{"type":"/errors/file.reference_unavailable","status":410,"retryable":false}`, wantCode: "file.reference_unavailable"},
		{name: "invalid_reference", status: http.StatusUnprocessableEntity, body: `{"type":"/errors/file.reference_invalid","status":422,"retryable":false}`, wantCode: "file.reference_invalid"},
		{name: "storage_unavailable", status: http.StatusServiceUnavailable, body: `{"type":"/errors/file.reference_storage_failed","status":503,"retryable":true}`, wantCode: "file.reference_storage_failed", wantRetry: true},
		{name: "truncated_response", status: http.StatusOK, truncated: true, wantCode: TypeUnreadableResponse, wantRetry: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.truncated {
					w.Header().Set("Content-Length", "20")
					_, _ = w.Write([]byte("partial"))
					return
				}
				w.Header().Set("Content-Type", ProblemContentType)
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			client := New(Config{BaseURL: srv.URL, HTTPClient: srv.Client()})
			_, err := client.ReadFileReferenceRuntime(t.Context(), "record", "payload", key.String(), 100)
			e, ok := errs.From(err)
			if !ok || e.Code() != tc.wantCode {
				t.Fatalf("error = %v, want code %q", err, tc.wantCode)
			}
			if got := errors.Is(err, errs.ErrRetryable); got != tc.wantRetry {
				t.Errorf("retryable = %v, want %v", got, tc.wantRetry)
			}
		})
	}
}
