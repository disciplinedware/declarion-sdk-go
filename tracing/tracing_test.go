package tracing

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

const parent = "00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01"

func recorder(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	saved := otel.GetTracerProvider()
	r := tracetest.NewSpanRecorder()
	p := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(r))
	otel.SetTracerProvider(p)
	t.Cleanup(func() { otel.SetTracerProvider(saved); require.NoError(t, p.Shutdown(context.Background())) })
	return r
}

func TestTraceContext(t *testing.T) {
	ctx := Restore(t.Context(), parent, "vendor=value")
	p, s := Serialize(ctx)
	require.Equal(t, parent, p)
	require.Equal(t, "vendor=value", s)
	for _, invalid := range []string{"", "garbage", "00-00000000000000000000000000000000-bbbbbbbbbbbbbbbb-01", "ff-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01"} {
		t.Run("invalid_"+invalid, func(t *testing.T) {
			p, s := Serialize(Restore(ctx, invalid, "vendor=value"))
			require.Empty(t, p)
			require.Empty(t, s)
		})
	}
}

func TestPathTrustBoundary(t *testing.T) {
	headers := http.Header{"Baggage": {"irrelevant=secret,declarion.trace_path=schedule:forged"}}
	ctx := WithWork(ExtractTrace(t.Context(), headers), "request", "http:POST")
	_, path := Work(ctx)
	require.Equal(t, "http:POST", path)
	ctx = AppendPath(AdoptPath(ctx, headers), "handler.code")
	out := http.Header{}
	Inject(ctx, out)
	require.Equal(t, "declarion.trace_path=schedule:forged->handler.code", out.Get("baggage"))
	for _, invalid := range []string{"secret\nvalue", "https://host/path", strings.Repeat("a", MaxTracePathBytes+1)} {
		require.Equal(t, "invalid", SanitizePath(invalid))
	}
	ctx = AppendPath(WithWork(ctx, "request", strings.Repeat("a", MaxTracePathBytes)), "handler")
	_, path = Work(ctx)
	require.Len(t, path, MaxTracePathBytes)
	require.True(t, strings.HasSuffix(path, "->truncated"))
}

func TestHTTPSpanPrivacyAndParent(t *testing.T) {
	r := recorder(t)
	h := ServerMiddleware(func(*http.Request) string { return "/api/data/{entity}" }, nil)(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { w.WriteHeader(http.StatusForbidden) }))
	req := httptest.NewRequest("GET", "http://host/secret-path?token=secret-value", nil)
	req.Header.Set("traceparent", parent)
	req.Header.Set("User-Agent", "secret-agent")
	h.ServeHTTP(httptest.NewRecorder(), req)
	spans := r.Ended()
	require.Len(t, spans, 1)
	require.Equal(t, "GET /api/data/{entity}", spans[0].Name())
	require.Equal(t, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", spans[0].SpanContext().TraceID().String())
	require.Equal(t, "bbbbbbbbbbbbbbbb", spans[0].Parent().SpanID().String())
	require.Equal(t, codes.Unset, spans[0].Status().Code)
	allowed := map[attribute.Key]bool{"http.request.method": true, "http.route": true, "http.response.status_code": true, "url.scheme": true, "error.type": true}
	for _, attr := range spans[0].Attributes() {
		require.True(t, allowed[attr.Key], string(attr.Key))
		require.NotContains(t, attr.Value.Emit(), "secret-")
	}
}

func TestHTTPClientPerSendAndRedirectPrivacy(t *testing.T) {
	r := recorder(t)
	headers := make(chan http.Header, 2)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		headers <- req.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(target.Close)
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		headers <- req.Header.Clone()
		http.Redirect(w, req, target.URL+"/secret-path?token=secret", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(source.Close)
	client := HTTPClient(source.Client(), source.URL)
	ctx := WithWork(Restore(t.Context(), parent, "vendor=value"), "request", "handler")
	req, err := http.NewRequestWithContext(ctx, "GET", source.URL, nil)
	require.NoError(t, err)
	resp, err := client.Do(req)
	require.NoError(t, err)
	_, err = io.Copy(io.Discard, resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	first, second := <-headers, <-headers
	require.NotEmpty(t, first.Get("traceparent"))
	require.Equal(t, "vendor=value", first.Get("tracestate"))
	require.Equal(t, "declarion.trace_path=handler", first.Get("baggage"))
	require.Empty(t, second.Get("traceparent"))
	require.Empty(t, second.Get("tracestate"))
	require.Empty(t, second.Get("baggage"))
	require.Len(t, r.Ended(), 2)
	for _, span := range r.Ended() {
		require.Equal(t, "GET", span.Name())
		for _, attr := range span.Attributes() {
			require.NotContains(t, attr.Value.Emit(), "secret")
		}
	}
}

func TestPlatformRedirectOriginUsesHostAndEffectivePort(t *testing.T) {
	for _, tc := range []struct {
		from, to    string
		wantHeaders bool
	}{
		{"https://example.com", "https://EXAMPLE.COM:443/callback", true},
		{"http://example.com:80", "http://example.com/callback", true},
		{"https://[::1]", "https://[0:0:0:0:0:0:0:1]:443/callback", true},
		{"https://example.com", "http://example.com/callback", false},
		{"https://example.com", "https://example.com:444/callback", false},
		{"https://example.com", "https://other.example/callback", false},
	} {
		t.Run(tc.to, func(t *testing.T) {
			destination, err := url.Parse(tc.from)
			require.NoError(t, err)
			request, err := http.NewRequestWithContext(t.Context(), "GET", tc.to, nil)
			require.NoError(t, err)
			Inject(Restore(t.Context(), parent, "vendor=value"), request.Header)
			client := PlatformClient(&http.Client{}, destination)
			require.NoError(t, client.CheckRedirect(request, nil))
			require.Equal(t, tc.wantHeaders, request.Header.Get("traceparent") != "")
			require.Equal(t, tc.wantHeaders, sameOrigin(destination, request.URL))
		})
	}
}

func TestContextLoggerUpdatesSpanWithoutDuplicateFields(t *testing.T) {
	recorder(t)
	core, observed := observer.New(zap.DebugLevel)
	base := zap.New(core)
	ctx := WithWork(Restore(t.Context(), parent, ""), "request", "handler")
	ctx, outer := otel.Tracer("test").Start(ctx, "outer")
	defer outer.End()
	logger := Logger(ctx, base).With(zap.String("trace_path", "wrong"))
	ctx, inner := otel.Tracer("test").Start(ctx, "inner")
	defer inner.End()
	Logger(ctx, logger).Info("work", zap.String("span_id", "wrong"), zap.String("model_request_id", "model-step"))
	entry := observed.All()[0]
	require.Equal(t, inner.SpanContext().SpanID().String(), entry.ContextMap()["span_id"])
	require.Equal(t, "handler", entry.ContextMap()["trace_path"])
	require.Equal(t, "model-step", entry.ContextMap()["model_request_id"])
	printed := 0
	for _, field := range entry.Context {
		if field.Type != zapcore.SkipType {
			printed++
		}
	}
	require.Equal(t, 5, printed, "each correlation key once, plus the caller's own field")
	Logger(WithWork(t.Context(), "request", "handler"), base).Info("root")
	require.NotContains(t, observed.All()[1].ContextMap(), "trace_id")
	require.False(t, trace.SpanContextFromContext(t.Context()).IsValid())
}
