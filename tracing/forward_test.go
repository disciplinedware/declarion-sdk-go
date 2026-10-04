package tracing

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func TestAForwardedRequestCarriesOurSpanAndKeepsTheCallersOtherHeaders(t *testing.T) {
	r := recorder(t)
	received := make(chan http.Header, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, req *http.Request) { received <- req.Header.Clone() }))
	defer upstream.Close()

	ctx, caller := otel.Tracer("test").Start(context.Background(), "gateway request")
	defer caller.End()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, upstream.URL+"/secret-path", nil)
	require.NoError(t, err)
	req.Header.Set("traceparent", "00-cccccccccccccccccccccccccccccccc-dddddddddddddddd-01")
	req.Header.Set("baggage", "agent=own")
	req.Header.Set("X-Agent", "kept")
	response, err := HTTPClient(&http.Client{}, "", ForwardTraceContext()).Do(req)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())

	headers := <-received
	client := r.Ended()[0]
	require.Equal(t, trace.SpanKindClient, client.SpanKind())
	require.Equal(t, "00-"+caller.SpanContext().TraceID().String()+"-"+client.SpanContext().SpanID().String()+"-01", headers.Get("traceparent"))
	require.Equal(t, "agent=own", headers.Get("baggage"), "the caller's baggage is its own request's, not ours to rewrite")
	require.Equal(t, "kept", headers.Get("X-Agent"))
	require.Equal(t, "00-cccccccccccccccccccccccccccccccc-dddddddddddddddd-01", req.Header.Get("traceparent"), "the caller's request is not mutated")
}

func TestTheContextLoggerHandsTheSpanToALogBridgeAndPrintsNothingForIt(t *testing.T) {
	recorder(t)
	ctx, span := otel.Tracer("test").Start(context.Background(), "work")
	defer span.End()
	var printed bytes.Buffer
	var handed context.Context
	bridge := bridgeCore{seen: func(fields []zapcore.Field) {
		for _, field := range fields {
			if carried, ok := field.Interface.(context.Context); ok {
				handed = carried
			}
		}
	}}
	text := zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()), zapcore.AddSync(&printed), zap.DebugLevel)
	Logger(ctx, zap.New(zapcore.NewTee(text, bridge))).Info("work")

	require.NotNil(t, handed, "the bridge receives the work's context")
	require.Equal(t, span.SpanContext().SpanID(), trace.SpanContextFromContext(handed).SpanID())
	require.Contains(t, printed.String(), span.SpanContext().TraceID().String())
	require.False(t, strings.Contains(printed.String(), `"context"`), "the text line carries no context field: %s", printed.String())
}

type bridgeCore struct {
	zapcore.LevelEnabler
	seen func([]zapcore.Field)
}

func (b bridgeCore) Enabled(zapcore.Level) bool        { return true }
func (b bridgeCore) With([]zapcore.Field) zapcore.Core { return b }
func (b bridgeCore) Sync() error                       { return nil }
func (b bridgeCore) Write(_ zapcore.Entry, fields []zapcore.Field) error {
	b.seen(fields)
	return nil
}
func (b bridgeCore) Check(entry zapcore.Entry, checked *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	return checked.AddCore(entry, b)
}
