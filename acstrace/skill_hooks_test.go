package acstrace

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestUnmappedSkillHooksUseTheirProtocolMethods(t *testing.T) {
	schemas, err := filepath.Glob("testdata/skill-*.json")
	require.NoError(t, err)
	require.Len(t, schemas, 3)
	for _, path := range schemas {
		t.Run(strings.ReplaceAll(strings.TrimSuffix(filepath.Base(path), ".json"), "-", "_"), func(t *testing.T) {
			raw, err := os.ReadFile(path)
			require.NoError(t, err)
			var schema struct {
				Title string `json:"title"`
			}
			require.NoError(t, json.Unmarshal(raw, &schema))
			method, ok := strings.CutSuffix(schema.Title, " payload")
			require.True(t, ok)
			definition, ok := Lookup(method)
			require.True(t, ok, method)
			require.Equal(t, method, definition.Name)
			require.Empty(t, definition.Required)
			recorder := tracetest.NewSpanRecorder()
			provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
			saved := otel.GetTracerProvider()
			otel.SetTracerProvider(provider)
			t.Cleanup(func() {
				otel.SetTracerProvider(saved)
				require.NoError(t, provider.Shutdown(context.WithoutCancel(t.Context())))
			})
			ctx, parent := provider.Tracer("test").Start(t.Context(), "server", trace.WithSpanKind(trace.SpanKindServer))
			_, step := Start(ctx, method,
				attribute.String("acs.session.id", "session"),
				attribute.String("acs.tenant_id", "tenant"),
				attribute.String("acs.provenance.origin", "system"),
				attribute.String("acs.provenance.source_id", "secret-source"),
				attribute.String("prompt", "secret-prompt"),
			)
			EmitDecision(step, Decision{Disposition: "allow", Evaluator: "deterministic"})
			step.End()
			parent.End()
			ended := recorder.Ended()[0]
			require.Equal(t, method, ended.Name())
			require.Equal(t, trace.SpanKindInternal, ended.SpanKind())
			require.Equal(t, parent.SpanContext().SpanID(), ended.Parent().SpanID())
			attributes := map[string]attribute.Value{}
			for _, attribute := range ended.Attributes() {
				attributes[string(attribute.Key)] = attribute.Value
				require.NotContains(t, attribute.Value.Emit(), "secret")
			}
			require.Equal(t, "session", attributes["acs.session.id"].AsString())
			require.Equal(t, "tenant", attributes["acs.tenant_id"].AsString())
			require.Equal(t, "system", attributes["acs.provenance.origin"].AsString())
			require.Equal(t, Hash("secret-source"), attributes["acs.provenance.source_id"].AsString())
			require.NotContains(t, attributes, "prompt")
			require.Len(t, ended.Events(), 1)
			require.Equal(t, "acs.decision", ended.Events()[0].Name)
		})
	}
}

func TestUnknownHooksDoNotUseUntrustedMethodNames(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	saved := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(saved)
		require.NoError(t, provider.Shutdown(context.WithoutCancel(t.Context())))
	})
	_, span := Start(t.Context(), "steps/skillRegister/secret-user-input")
	span.End()
	require.Empty(t, recorder.Ended())
}
