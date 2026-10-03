package acstrace

import (
	"context"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"testing"
)

func TestMappingAndDecisionPrivacy(t *testing.T) {
	saved := otel.GetTracerProvider()
	r := tracetest.NewSpanRecorder()
	p := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(r))
	otel.SetTracerProvider(p)
	t.Cleanup(func() { otel.SetTracerProvider(saved); require.NoError(t, p.Shutdown(context.Background())) })
	require.Len(t, definitions, 18)
	for method, definition := range definitions {
		t.Run(method, func(t *testing.T) {
			attrs := []attribute.KeyValue{attribute.String("prompt", "secret-prompt"), attribute.String("acs.provenance.source_id", "secret-source")}
			for _, key := range definition.Required {
				attrs = append(attrs, attribute.String(key, "value"))
			}
			_, span := Start(t.Context(), method, attrs...)
			EmitDecision(span, Decision{Disposition: "deny", Evaluator: "rules", Reasoning: "secret-reasoning"})
			span.End()
			ended := r.Ended()[len(r.Ended())-1]
			require.Equal(t, definition.Name, ended.Name())
			keys := map[string]bool{}
			for _, attr := range ended.Attributes() {
				keys[string(attr.Key)] = true
				require.NotContains(t, attr.Value.Emit(), "secret")
			}
			for _, key := range definition.Required {
				require.True(t, keys[key], key)
			}
			require.False(t, keys["prompt"])
			require.Len(t, ended.Events(), 1)
			require.Equal(t, "acs.decision", ended.Events()[0].Name)
			for _, attr := range ended.Events()[0].Attributes {
				require.NotContains(t, attr.Value.Emit(), "secret")
			}
		})
	}
}
