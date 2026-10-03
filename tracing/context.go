package tracing

import (
	"context"
	"net/http"
	"strings"

	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

const TracePathMember = "declarion.trace_path"
const MaxTracePathBytes = 256

var Propagator = propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{})

type workKey struct{}
type work struct{ RequestID, TracePath string }

func WithWork(ctx context.Context, requestID, path string) context.Context {
	return context.WithValue(ctx, workKey{}, work{RequestID: requestID, TracePath: SanitizePath(path)})
}

func Work(ctx context.Context) (requestID, path string) {
	w, _ := ctx.Value(workKey{}).(work)
	return w.RequestID, w.TracePath
}

func SanitizePath(path string) string {
	if len(path) > MaxTracePathBytes {
		return "invalid"
	}
	for i := 0; i < len(path); i++ {
		c := path[i]
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || strings.ContainsRune("._:-", rune(c)) {
			continue
		}
		if c == '>' && i > 0 && path[i-1] == '-' {
			continue
		}
		return "invalid"
	}
	return path
}

func AppendPath(ctx context.Context, code string) context.Context {
	id, path := Work(ctx)
	code = SanitizePath(code)
	if path == "" {
		return WithWork(ctx, id, code)
	}
	joined := path + "->" + code
	if len(joined) > MaxTracePathBytes {
		joined = joined[:MaxTracePathBytes-len("->truncated")] + "->truncated"
	}
	return WithWork(ctx, id, joined)
}

// ExtractTrace excludes Baggage until the protocol owner authenticates the peer.
func ExtractTrace(ctx context.Context, headers http.Header) context.Context {
	ctx = trace.ContextWithSpanContext(ctx, trace.SpanContext{})
	return propagation.TraceContext{}.Extract(ctx, propagation.HeaderCarrier(headers))
}

func AdoptPath(ctx context.Context, headers http.Header) context.Context {
	extracted := propagation.Baggage{}.Extract(ctx, propagation.HeaderCarrier(headers))
	path := baggage.FromContext(extracted).Member(TracePathMember).Value()
	if path == "" {
		return ctx
	}
	id, _ := Work(ctx)
	return WithWork(ctx, id, path)
}

func Inject(ctx context.Context, headers http.Header) {
	Strip(headers)
	propagation.TraceContext{}.Inject(ctx, propagation.HeaderCarrier(headers))
	_, path := Work(ctx)
	if path != "" {
		member, err := baggage.NewMember(TracePathMember, SanitizePath(path))
		if err == nil {
			bag, err := baggage.New(member)
			if err == nil {
				propagation.Baggage{}.Inject(baggage.ContextWithBaggage(ctx, bag), propagation.HeaderCarrier(headers))
			}
		}
	}
}

func Strip(headers http.Header) {
	headers.Del("traceparent")
	headers.Del("tracestate")
	headers.Del("baggage")
}

func Serialize(ctx context.Context) (parent, state string) {
	carrier := propagation.MapCarrier{}
	propagation.TraceContext{}.Inject(ctx, carrier)
	return carrier.Get("traceparent"), carrier.Get("tracestate")
}

func Restore(ctx context.Context, parent, state string) context.Context {
	ctx = trace.ContextWithSpanContext(ctx, trace.SpanContext{})
	return propagation.TraceContext{}.Extract(ctx, propagation.MapCarrier{"traceparent": parent, "tracestate": state})
}
