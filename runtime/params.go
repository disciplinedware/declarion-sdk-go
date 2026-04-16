package runtime

import "github.com/disciplinedware/declarion-sdk-go/platform"

// GetParam retrieves a platform parameter with type-safe conversion.
// The platform resolves env var overrides server-side (via env_var in parameter YAML).
//
//	token, err := runtime.GetParam[string](ctx, "clickup_api_token")
//	maxRetries, err := runtime.GetParam[int](ctx, "max_retries")
//	enabled, err := runtime.GetParam[bool](ctx, "feature_flag")
func GetParam[T any](ctx *Ctx, code string) (T, error) {
	return platform.GetParam[T](ctx.Platform.Params(), ctx.Context, code)
}
