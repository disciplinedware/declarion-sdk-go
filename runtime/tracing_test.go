package runtime

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/disciplinedware/declarion-sdk-go/errs"
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

func TestRPCFailuresCarryBoundedErrorTypes(t *testing.T) {
	saved := otel.GetTracerProvider()
	r := tracetest.NewSpanRecorder()
	p := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(r))
	otel.SetTracerProvider(p)
	t.Cleanup(func() { otel.SetTracerProvider(saved); require.NoError(t, p.Shutdown(context.Background())) })
	savedCatalogue := errs.ProcessRenderContext("en", "", 0)
	errs.SetCatalogue(errs.Catalogue{"test.declared": &errs.TypeDef{Status: 409}}, "en")
	t.Cleanup(func() { errs.SetCatalogue(savedCatalogue.Catalogue, savedCatalogue.DefaultLocale) })
	core, observed := observer.New(zap.DebugLevel)
	ClearHandlerRegistry()
	t.Cleanup(ClearHandlerRegistry)
	RegisterHandler[echoParams, echoResult]("test.panic", func(*HandlerCtx, echoParams) (echoResult, error) {
		panic("secret-panic-value")
	})
	RegisterHandler[echoParams, echoResult]("test.undeclared", func(*HandlerCtx, echoParams) (echoResult, error) {
		return echoResult{}, errs.New("test.secret-dynamic-42")
	})
	RegisterHandler[echoParams, echoResult]("test.declared", func(*HandlerCtx, echoParams) (echoResult, error) {
		return echoResult{}, errs.New("test.declared")
	})
	handler, err := NewHandler(Config{JWTSecret: testSecret, RequireToken: true, Logger: zap.New(core)})
	require.NoError(t, err)
	for _, tc := range []struct {
		method, wantType, wantStatus string
	}{
		{"test.panic", "-32603", "-32603"},
		{"test.undeclared", "-32000", "-32000"},
		{"test.declared", "test.declared", "-32000"},
	} {
		t.Run(tc.method, func(t *testing.T) {
			body := `{"jsonrpc":"2.0","id":"1","method":"` + tc.method + `","params":{}}`
			req := httptest.NewRequest(http.MethodPost, "http://sidecar/rpc", strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+mintTestToken(t, "tenant", "user", tc.method, "audit"))
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
			require.Equal(t, http.StatusOK, w.Code)
			var resp Response
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			require.NotNil(t, resp.Error)
			require.Equal(t, tc.wantStatus, strconv.Itoa(resp.Error.Code))
			span := r.Ended()[len(r.Ended())-1]
			require.Equal(t, codes.Error, span.Status().Code)
			attrs := map[string]string{}
			for _, attr := range span.Attributes() {
				attrs[string(attr.Key)] = attr.Value.Emit()
				require.NotContains(t, attr.Value.Emit(), "secret")
			}
			require.Equal(t, tc.wantType, attrs["error.type"])
			require.Equal(t, tc.wantStatus, attrs["rpc.response.status_code"])
		})
	}
	panicked := observed.FilterMessage("request failed").All()
	require.Len(t, panicked, 1)
	logged := panicked[0].ContextMap()
	require.Contains(t, logged["error"], "secret-panic-value")
	require.Contains(t, logged["error"], "runtime/debug.Stack")
	require.NotEmpty(t, logged["trace_id"])
}
