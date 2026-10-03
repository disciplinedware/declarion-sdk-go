package tracing

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHTTPMethodsDoNotExposeUnrecognizedTokens(t *testing.T) {
	for _, tc := range []struct {
		method string
		name   string
		value  string
	}{
		{http.MethodGet, "GET", "GET"},
		{"QUERY", "QUERY", "QUERY"},
		{"get", "HTTP", "_OTHER"},
		{strings.Repeat("secret", 1000), "HTTP", "_OTHER"},
	} {
		t.Run(tc.name+tc.method[:1], func(t *testing.T) {
			r := recorder(t)
			server := httptest.NewServer(ServerMiddleware(nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				require.Equal(t, tc.method, req.Method)
				_, path := Work(req.Context())
				require.Equal(t, "http:"+tc.name, path)
				w.WriteHeader(http.StatusNoContent)
			})))
			t.Cleanup(server.Close)
			request, err := http.NewRequestWithContext(t.Context(), tc.method, server.URL, nil)
			require.NoError(t, err)
			response, err := HTTPClient(server.Client(), "").Do(request)
			require.NoError(t, err)
			_, err = io.Copy(io.Discard, response.Body)
			require.NoError(t, err)
			require.NoError(t, response.Body.Close())
			require.Len(t, r.Ended(), 2)
			for _, span := range r.Ended() {
				require.Equal(t, tc.name, span.Name())
				for _, attr := range span.Attributes() {
					if attr.Key == "http.request.method" {
						require.Equal(t, tc.value, attr.Value.AsString())
					}
					require.NotContains(t, attr.Value.String(), "secret")
				}
			}
		})
	}
}

func TestKnownHTTPMethodsAreACompleteOverride(t *testing.T) {
	t.Setenv("OTEL_INSTRUMENTATION_HTTP_KNOWN_METHODS", "PROPFIND")
	for _, tc := range []struct{ method, value, name string }{
		{"PROPFIND", "PROPFIND", "PROPFIND"},
		{http.MethodGet, "_OTHER", "HTTP"},
		{"propfind", "_OTHER", "HTTP"},
	} {
		value, name := httpMethod(tc.method, knownHTTPMethods())
		require.Equal(t, tc.value, value)
		require.Equal(t, tc.name, name)
	}
}
