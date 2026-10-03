package tracing

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/codes"
)

func TestServerTracingPreservesResponseAndFirstStatus(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		write  func(http.ResponseWriter)
	}{
		{"implicit_ok", 200, func(w http.ResponseWriter) { _, _ = w.Write([]byte("body")) }},
		{"forbidden", 403, func(w http.ResponseWriter) { w.WriteHeader(403); _, _ = w.Write([]byte("body")) }},
		{"server_error", 503, func(w http.ResponseWriter) { w.WriteHeader(503); _, _ = w.Write([]byte("body")) }},
		{"first_status", 202, func(w http.ResponseWriter) { w.WriteHeader(202); w.WriteHeader(503); _, _ = w.Write([]byte("body")) }},
		{"flush", 200, func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.(http.Flusher).Flush()
			_, _ = w.Write([]byte("body"))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorded := recorder(t)
			handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { tc.write(w) })
			server := httptest.NewServer(ServerMiddleware(nil, nil)(handler))
			t.Cleanup(server.Close)
			request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL, nil)
			require.NoError(t, err)
			response, err := server.Client().Do(request)
			require.NoError(t, err)
			body, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			require.NoError(t, response.Body.Close())
			require.Equal(t, tc.status, response.StatusCode)
			require.Equal(t, "body", string(body))
			require.Eventually(t, func() bool { return len(recorded.Ended()) == 1 }, time.Second, time.Millisecond)
			span := recorded.Ended()[0]
			want := codes.Unset
			if tc.status >= 500 {
				want = codes.Error
			}
			require.Equal(t, want, span.Status().Code)
			for _, attr := range span.Attributes() {
				if attr.Key == "http.response.status_code" {
					require.Equal(t, int64(tc.status), attr.Value.AsInt64())
				}
			}
		})
	}
}
