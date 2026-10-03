package acstrace

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestDecisionEvaluatorsMatchTheACSResponseSchema(t *testing.T) {
	raw, err := os.ReadFile("testdata/response-envelope.json")
	require.NoError(t, err)
	var schema map[string]any
	require.NoError(t, json.Unmarshal(raw, &schema))
	definitions := schema["$defs"].(map[string]any)
	result := definitions["AcsResult"].(map[string]any)["properties"].(map[string]any)
	metadata := result["metadata"].(map[string]any)["properties"].(map[string]any)
	evaluators := metadata["evaluator"].(map[string]any)["enum"].([]any)
	for _, evaluator := range append(evaluators, "", "guardian", "human", "rules") {
		t.Run(evaluator.(string), func(t *testing.T) {
			recorder := tracetest.NewSpanRecorder()
			provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
			t.Cleanup(func() { require.NoError(t, provider.Shutdown(context.WithoutCancel(t.Context()))) })
			_, span := provider.Tracer("test").Start(t.Context(), "step")
			EmitDecision(span, Decision{Disposition: "allow", Evaluator: evaluator.(string), EvaluatorVersion: "1", ModelID: "judge"})
			span.End()
			ended := recorder.Ended()[0]
			valid := false
			for _, allowed := range evaluators {
				valid = valid || evaluator == allowed
			}
			if !valid {
				require.Empty(t, ended.Events())
				if evaluator == "" {
					require.Equal(t, codes.Unset, ended.Status().Code)
				} else {
					require.Equal(t, codes.Error, ended.Status().Code)
				}
				return
			}
			require.Len(t, ended.Events(), 1)
			attrs := map[string]string{}
			for _, attr := range ended.Events()[0].Attributes {
				attrs[string(attr.Key)] = attr.Value.AsString()
			}
			require.Equal(t, evaluator, attrs["acs.evaluator"])
			require.Equal(t, "1", attrs["acs.evaluator_version"])
			require.Equal(t, "judge", attrs["acs.model_id"])
		})
	}
	for _, evaluator := range []string{"agent", "composite"} {
		t.Run(evaluator+"_missing_model", func(t *testing.T) {
			recorder := tracetest.NewSpanRecorder()
			provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
			t.Cleanup(func() { require.NoError(t, provider.Shutdown(context.WithoutCancel(t.Context()))) })
			_, span := provider.Tracer("test").Start(t.Context(), "step")
			EmitDecision(span, Decision{Disposition: "deny", Evaluator: evaluator})
			span.End()
			require.Empty(t, recorder.Ended()[0].Events())
			require.Equal(t, codes.Error, recorder.Ended()[0].Status().Code)
		})
	}
}

func TestMappingAndDecisionPrivacy(t *testing.T) {
	saved := otel.GetTracerProvider()
	r := tracetest.NewSpanRecorder()
	p := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(r))
	otel.SetTracerProvider(p)
	t.Cleanup(func() { otel.SetTracerProvider(saved); require.NoError(t, p.Shutdown(context.Background())) })
	require.Len(t, definitions, 21)
	for method, definition := range definitions {
		t.Run(method, func(t *testing.T) {
			attrs := []attribute.KeyValue{attribute.String("prompt", "secret-prompt"), attribute.String("acs.provenance.source_id", "secret-source")}
			for _, key := range definition.Required {
				attrs = append(attrs, attribute.String(key, "value"))
			}
			_, span := Start(t.Context(), method, attrs...)
			EmitDecision(span, Decision{Disposition: "deny", Evaluator: "deterministic", Reasoning: "secret-reasoning"})
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
