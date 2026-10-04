package tracing

import (
	"context"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type contextCore struct {
	zapcore.Core
	ctx context.Context
}

func correlationKey(key string) bool {
	return key == "trace_id" || key == "span_id" || key == "request_id" || key == "trace_path"
}

func withoutCorrelation(fields []zapcore.Field) []zapcore.Field {
	filtered := make([]zapcore.Field, 0, len(fields))
	for _, field := range fields {
		if !correlationKey(field.Key) {
			filtered = append(filtered, field)
		}
	}
	return filtered
}

func Logger(ctx context.Context, base *zap.Logger) *zap.Logger {
	return base.WithOptions(zap.WrapCore(func(core zapcore.Core) zapcore.Core {
		if previous, ok := core.(*contextCore); ok {
			core = previous.Core
		}
		return &contextCore{Core: core, ctx: ctx}
	}))
}

func (c *contextCore) With(fields []zapcore.Field) zapcore.Core {
	return &contextCore{Core: c.Core.With(withoutCorrelation(fields)), ctx: c.ctx}
}

func (c *contextCore) Check(entry zapcore.Entry, checked *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if c.Core.Check(entry, nil) != nil {
		return checked.AddCore(entry, c)
	}
	return checked
}

func (c *contextCore) Write(entry zapcore.Entry, fields []zapcore.Field) error {
	fields = withoutCorrelation(fields)
	sc := trace.SpanContextFromContext(c.ctx)
	if sc.IsValid() {
		fields = append(fields, zap.String("trace_id", sc.TraceID().String()), zap.String("span_id", sc.SpanID().String()),
			// Invisible to text encoders; an OpenTelemetry log bridge such as otelzap
			// takes it as the record's context and sets its trace and span ids.
			zap.Field{Key: "context", Type: zapcore.SkipType, Interface: c.ctx})
	}
	id, path := Work(c.ctx)
	if id != "" {
		fields = append(fields, zap.String("request_id", id))
	}
	if path != "" {
		fields = append(fields, zap.String("trace_path", path))
	}
	return c.Core.Write(entry, fields)
}
