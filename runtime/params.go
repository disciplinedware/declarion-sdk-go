package runtime

import (
	"fmt"

	"github.com/disciplinedware/declarion-sdk-go/platform"
)

// GetParam retrieves an OPTIONAL platform parameter with a caller-supplied
// default. The default is the value used when the platform reports "not
// found" (parameter undeclared, restricted-category, or no value at any
// resolution layer). The default never crosses the network; the server only
// answers "do you have a value for code X?".
//
// Behaviour:
//   - Platform has a value      -> (value, nil).
//   - Platform reports no value  -> (def, nil). No error.
//   - Coercion or transport err  -> (zero, AppError{CodeInternal}). The
//     returned error is already final and code-tagged ("resolve parameter
//     "X": ..."); callers MUST propagate it as-is (return err) - never
//     re-wrap with the parameter name, that only duplicates it.
//
//	model, err := runtime.GetParam[string](ctx, "parse_model", "gpt-5")
//	enabled, err := runtime.GetParam[bool](ctx, "feature_flag", false)
func GetParam[T any](ctx *Ctx, code string, def T) (T, error) {
	v, err := platform.GetParam[T](ctx.Platform.Params(), ctx.Context, code, def)
	if err != nil {
		var zero T
		return zero, paramResolveError(code, err)
	}
	return v, nil
}

// GetRequiredParam retrieves a REQUIRED parameter of any type. Unlike GetParam
// there is no default: it is an error iff the platform has NO value for the
// code (not found). A value that IS configured is returned as-is, even when
// it is "", 0, or false - those are valid configured values, never treated as
// absence (this is why the check is found-based, not empty-based). The two
// failure modes - the fetch failing (transport / platform unreachable) and
// the value being absent - both return a final, code-tagged AppError; the
// caller propagates it as-is.
//
//	token, err := runtime.GetRequiredParam[string](ctx, "clickup_api_token")
//	if err != nil { return Result{}, err }
func GetRequiredParam[T any](ctx *Ctx, code string) (T, error) {
	var zero T
	value, found, _, err := ctx.Platform.Params().Lookup(ctx.Context, code)
	if err != nil {
		return zero, paramResolveError(code, err)
	}
	if !found {
		return zero, &AppError{
			Code:          JSONRPCAppError,
			Message:       fmt.Sprintf("required parameter %q is not configured", code),
			DeclarionCode: CodeInternal,
		}
	}
	v, cerr := platform.Convert[T](value)
	if cerr != nil {
		return zero, paramResolveError(code, cerr)
	}
	return v, nil
}

// paramResolveError is the single shape for a param fetch that fails for an
// infrastructure reason (transport / coercion). The parameter code is baked
// in, so callers propagate the error as-is and never re-name it.
func paramResolveError(code string, err error) *AppError {
	return &AppError{
		Code:          JSONRPCAppError,
		Message:       fmt.Sprintf("resolve parameter %q: %s", code, err),
		DeclarionCode: CodeInternal,
	}
}
