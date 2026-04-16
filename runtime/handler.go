package runtime

import (
	"encoding/json"
	"fmt"
)

// HandlerFunc is the function signature for a typed handler.
// P is the params type, R is the result type.
type HandlerFunc[P any, R any] func(ctx *Ctx, params P) (R, error)

// HandlerRegistration is a method-name to dispatch-function mapping.
type HandlerRegistration struct {
	Method  string
	Dispatch func(ctx *Ctx, rawParams json.RawMessage) (any, error)
}

// Handler creates a HandlerRegistration from a typed handler function.
// The method name is the JSON-RPC method the platform will call (e.g. "clickup.fetch").
func Handler[P any, R any](method string, fn HandlerFunc[P, R]) HandlerRegistration {
	return HandlerRegistration{
		Method: method,
		Dispatch: func(ctx *Ctx, rawParams json.RawMessage) (any, error) {
			var params P
			if len(rawParams) > 0 {
				if err := json.Unmarshal(rawParams, &params); err != nil {
					return nil, &AppError{
						Code:          JSONRPCInvalidParams,
						Message:       fmt.Sprintf("invalid params: %s", err),
						DeclarionCode: CodeValidation,
					}
				}
			}
			return fn(ctx, params)
		},
	}
}

// AppError is an application-level error that maps to a JSON-RPC error response.
// Handlers return this to control the error code, message, and Declarion code.
type AppError struct {
	Code          int
	Message       string
	DeclarionCode string
	Retryable     bool
}

func (e *AppError) Error() string {
	if e.DeclarionCode != "" {
		return fmt.Sprintf("[%s] %s", e.DeclarionCode, e.Message)
	}
	return e.Message
}
