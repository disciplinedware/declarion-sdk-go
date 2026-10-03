package acstrace

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestProvenanceAggregationAndEmission(t *testing.T) {
	for _, test := range []struct {
		name       string
		items      []Provenance
		origins    any
		sourceHash string
	}{
		{name: "empty"},
		{name: "missing_origin", items: []Provenance{{SourceID: "private-source"}}},
		{name: "single_source", items: []Provenance{{Origin: "user_input", SourceID: "private-source"}}, origins: "user_input", sourceHash: "831a93071a15afdd34c7ad86d5e00071639c24bffcddf1e1d6b53b8666417820"},
		{name: "same_source_repeated", items: []Provenance{{Origin: "user_input", SourceID: "private-source"}, {Origin: "user_input", SourceID: "private-source"}}, origins: "user_input", sourceHash: "831a93071a15afdd34c7ad86d5e00071639c24bffcddf1e1d6b53b8666417820"},
		{name: "mixed_origins", items: []Provenance{{Origin: "user_input"}, {}, {Origin: "agent_generated"}}, origins: []string{"agent_generated", "user_input"}},
		{name: "multiple_sources", items: []Provenance{{Origin: "user_input", SourceID: "source-a"}, {Origin: "user_input", SourceID: "source-b"}}, origins: "user_input"},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := tracetest.NewSpanRecorder()
			provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
			saved := otel.GetTracerProvider()
			otel.SetTracerProvider(provider)
			t.Cleanup(func() {
				otel.SetTracerProvider(saved)
				require.NoError(t, provider.Shutdown(context.WithoutCancel(t.Context())))
			})
			_, span := Start(t.Context(), "steps/toolCallRequest", ProvenanceAttributes(test.items)...)
			span.End()
			require.Len(t, recorder.Ended(), 1)
			attributes := map[string]attribute.Value{}
			for _, attr := range recorder.Ended()[0].Attributes() {
				attributes[string(attr.Key)] = attr.Value
				require.NotContains(t, attr.Value.Emit(), "private")
			}
			if test.origins == nil {
				require.NotContains(t, attributes, "acs.provenance.origin")
			} else {
				require.Equal(t, test.origins, attributes["acs.provenance.origin"].AsInterface())
			}
			if test.sourceHash == "" {
				require.NotContains(t, attributes, "acs.provenance.source_id")
			} else {
				require.Equal(t, test.sourceHash, attributes["acs.provenance.source_id"].AsString())
			}
		})
	}
}
