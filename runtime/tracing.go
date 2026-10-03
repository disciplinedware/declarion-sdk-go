package runtime

import (
	"context"
	"strings"

	"github.com/disciplinedware/declarion-sdk-go/tracing"
)

func handlerTracePath(ctx context.Context, method string) context.Context {
	_, path := tracing.Work(ctx)
	if path == method || strings.HasSuffix(path, "->"+method) {
		return ctx
	}
	return tracing.AppendPath(ctx, method)
}
