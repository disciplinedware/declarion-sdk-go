package platform

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/disciplinedware/declarion-sdk-go/tracing"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestSharedClientUsesEachBufferedAndStreamingContext(t *testing.T) {
	const first = "00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01"
	const second = "00-cccccccccccccccccccccccccccccccc-dddddddddddddddd-00"
	headers := make(chan http.Header, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers <- r.Header.Clone()
		if r.URL.Path == "/api/actions/record.read" {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(startBlock + dataBlock(`{"result":true}`) + endSuccess))
		} else {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[]}`))
		}
	}))
	t.Cleanup(srv.Close)
	client := New(Config{BaseURL: srv.URL, HTTPClient: srv.Client()})
	for _, parent := range []string{first, second} {
		ctx := tracing.WithWork(tracing.Restore(t.Context(), parent, "vendor=value"), "request", "handler")
		_, err := client.Data().List(ctx, "record", ListParams{})
		require.NoError(t, err)
		stream, err := client.Actions().InvokeStreaming(ctx, "record.read", InvokeParams{})
		require.NoError(t, err)
		frames, err := parseStream(stream)
		require.NoError(t, err)
		require.Equal(t, []string{`{"result":true}`}, frames)
		for range 2 {
			h := <-headers
			require.Equal(t, parent, h.Get("traceparent"))
			require.Equal(t, "vendor=value", h.Get("tracestate"))
			require.Equal(t, "declarion.trace_path=handler", h.Get("baggage"))
		}
	}
}

func TestPlatformHTTPSpanUsesFixedTemplateAndOmitsUnknownPath(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	saved := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(saved)
		require.NoError(t, provider.Shutdown(context.WithoutCancel(t.Context())))
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NotEmpty(t, r.Header.Get("traceparent"))
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client := New(Config{BaseURL: server.URL + "/prefix", HTTPClient: server.Client()})
	for _, path := range []string{"/api/data/secret-entity", "/api/actions/secret-code", "/secret-unknown"} {
		_, status, _, err := client.do(t.Context(), http.MethodGet, path, nil, nil)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, status)
	}
	spans := recorder.Ended()
	require.Len(t, spans, 3)
	require.Equal(t, "GET /api/data/{entity}", spans[0].Name())
	require.Equal(t, "GET /api/actions/{code}", spans[1].Name())
	require.Equal(t, "GET", spans[2].Name())
	for _, span := range spans {
		for _, attr := range span.Attributes() {
			require.NotContains(t, attr.Value.String(), "secret-")
		}
	}
}
