package acstrace

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

//go:embed testdata/otel-mapping.json
var mappingJSON []byte

type Definition struct {
	Name     string   `json:"span_name"`
	Required []string `json:"required_attributes"`
	Optional []string `json:"optional_attributes"`
}

var definitions = loadMapping()

func loadMapping() map[string]Definition {
	var document struct {
		Properties struct {
			Steps struct {
				Default map[string]Definition `json:"default"`
			} `json:"step_to_span"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(mappingJSON, &document); err != nil {
		panic(err)
	}
	return document.Properties.Steps.Default
}

func Lookup(method string) (Definition, bool) {
	d, ok := definitions[method]
	d.Required = append([]string(nil), d.Required...)
	d.Optional = append([]string(nil), d.Optional...)
	return d, ok
}

func Hash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func Start(ctx context.Context, method string, attributes ...attribute.KeyValue) (context.Context, trace.Span) {
	d, ok := definitions[method]
	if !ok {
		return ctx, trace.SpanFromContext(context.Background())
	}
	allowed := make(map[string]bool, len(d.Required)+len(d.Optional)+3)
	for _, key := range d.Required {
		allowed[key] = true
	}
	for _, key := range d.Optional {
		allowed[key] = true
	}
	for _, key := range []string{"acs.provenance.origin", "acs.provenance.lineage_depth", "acs.provenance.source_id"} {
		allowed[key] = true
	}
	filtered := make([]attribute.KeyValue, 0, len(attributes))
	for _, attr := range attributes {
		if !allowed[string(attr.Key)] {
			continue
		}
		if attr.Key == "acs.provenance.source_id" {
			attr = attr.Key.String(Hash(attr.Value.AsString()))
		}
		filtered = append(filtered, attr)
	}
	return otel.Tracer("github.com/disciplinedware/declarion-sdk-go/acstrace").Start(ctx, d.Name, trace.WithSpanKind(trace.SpanKindInternal), trace.WithAttributes(filtered...))
}

type Decision struct {
	Disposition string
	Evaluator   string
	Reasoning   string
	Confidence  *float64
}

func EmitDecision(span trace.Span, decision Decision) {
	attrs := []attribute.KeyValue{attribute.String("acs.decision", decision.Disposition), attribute.String("acs.evaluator", decision.Evaluator)}
	if decision.Reasoning != "" {
		attrs = append(attrs, attribute.String("acs.reasoning", Hash(decision.Reasoning)))
	}
	if decision.Confidence != nil {
		attrs = append(attrs, attribute.Float64("acs.confidence", *decision.Confidence))
	}
	span.AddEvent("acs.decision", trace.WithAttributes(attrs...))
}
