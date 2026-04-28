package runtime

import "github.com/disciplinedware/declarion-sdk-go/platform"

// GetParam retrieves a platform parameter with a caller-supplied default.
// The default is the safety net used when the platform reports "not found"
// (parameter undeclared, restricted-category, or no value at any
// resolution layer). The default never crosses the network; the server
// only answers "do you have a value for code X?".
//
// Behaviour:
//   - Platform has a value     -> (value, nil).
//   - Platform reports no value -> (def, nil). No error.
//   - Coercion or transport err -> (zero, err). Caller MUST propagate.
//
//	token, err := runtime.GetParam[string](ctx, "clickup_api_token", "")
//	workers, err := runtime.GetParam[int](ctx, "clickup_comment_workers", 5)
//	enabled, err := runtime.GetParam[bool](ctx, "feature_flag", false)
func GetParam[T any](ctx *Ctx, code string, def T) (T, error) {
	return platform.GetParam[T](ctx.Platform.Params(), ctx.Context, code, def)
}
