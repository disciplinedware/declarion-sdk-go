package platform

import (
	"github.com/disciplinedware/declarion-sdk-go/tracing"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"testing"
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
