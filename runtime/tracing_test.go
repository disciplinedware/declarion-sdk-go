package runtime

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestRPCTracingIncludesAuthenticatedWorkAndEarlyFailures(t *testing.T) {
	saved := otel.GetTracerProvider()
	r := tracetest.NewSpanRecorder()
	p := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(r))
	otel.SetTracerProvider(p)
	t.Cleanup(func() { otel.SetTracerProvider(saved); require.NoError(t, p.Shutdown(context.Background())) })
	core, observed := observer.New(zap.DebugLevel)
	ClearHandlerRegistry()
	t.Cleanup(ClearHandlerRegistry)
	var contexts []trace.SpanContext
	RegisterHandler[echoParams, echoResult]("test.trace", func(ctx *HandlerCtx, _ echoParams) (echoResult, error) {
		contexts = append(contexts, trace.SpanContextFromContext(ctx.Context))
		ctx.Logger.Info("work")
		return echoResult{Message: "ok"}, nil
	})
	handler, err := NewHandler(Config{JWTSecret: testSecret, RequireToken: true, Logger: zap.New(core)})
	require.NoError(t, err)
	for _, tc := range []struct {
		name, body, token string
		status            codes.Code
	}{
		{"valid", `{"jsonrpc":"2.0","id":"1","method":"test.trace","params":{}}`, mintTestToken(t, "tenant", "user", "test.trace", "audit"), codes.Unset},
		{"already_entered_method", `{"jsonrpc":"2.0","id":"1","method":"test.trace","params":{}}`, mintTestToken(t, "tenant", "user", "test.trace", "audit"), codes.Unset},
		{"recursive_dispatch", `{"jsonrpc":"2.0","id":"1","method":"test.trace","params":{}}`, mintTestToken(t, "tenant", "user", "test.trace", "audit"), codes.Unset},
		{"invalid_token", `{"jsonrpc":"2.0","id":"1","method":"test.trace","params":{}}`, "invalid", codes.Error},
		{"invalid_body", `{`, "invalid", codes.Error},
		{"unknown_method", `{"jsonrpc":"2.0","id":"1","method":"secret-token-in-method","params":{}}`, mintTestToken(t, "tenant", "user", "secret-token-in-method", "audit"), codes.Error},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "http://sidecar/rpc", strings.NewReader(tc.body))
			req.Header.Set("traceparent", "00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01")
			req.Header.Set("baggage", "declarion.trace_path=upstream,other=secret")
			if tc.name == "already_entered_method" {
				req.Header.Set("baggage", "declarion.trace_path=upstream->test.trace,other=secret")
			}
			if tc.name == "recursive_dispatch" {
				req.Header.Set("baggage", "declarion.trace_path=upstream->test.trace->test.trace,other=secret")
			}
			req.Header.Set("Authorization", "Bearer "+tc.token)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
			response := w.Result()
			defer func() { require.NoError(t, response.Body.Close()) }()
			_, err := io.Copy(io.Discard, response.Body)
			require.NoError(t, err)
			span := r.Ended()[len(r.Ended())-1]
			require.Equal(t, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", span.SpanContext().TraceID().String())
			require.Equal(t, "bbbbbbbbbbbbbbbb", span.Parent().SpanID().String())
			require.Equal(t, tc.status, span.Status().Code)
			require.NotContains(t, span.Name(), "secret-token-in-method")
			for _, attr := range span.Attributes() {
				require.NotContains(t, attr.Value.Emit(), "secret-token-in-method")
			}
		})
	}
	require.Len(t, contexts, 3)
	require.True(t, contexts[0].IsValid())
	work := observed.FilterMessage("work").All()[0].ContextMap()
	require.Equal(t, contexts[0].SpanID().String(), work["span_id"])
	require.Equal(t, "upstream->test.trace", work["trace_path"])
	repeated := observed.FilterMessage("work").All()[1].ContextMap()
	require.Equal(t, contexts[1].SpanID().String(), repeated["span_id"])
	require.Equal(t, "upstream->test.trace", repeated["trace_path"])
	recursive := observed.FilterMessage("work").All()[2].ContextMap()
	require.Equal(t, contexts[2].SpanID().String(), recursive["span_id"])
	require.Equal(t, "upstream->test.trace->test.trace", recursive["trace_path"])
	for _, entry := range observed.FilterMessage("request failed").All() {
		require.Equal(t, "http:POST", entry.ContextMap()["trace_path"])
	}
}
