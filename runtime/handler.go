package runtime

import (
	"fmt"
)

// HandlerFunc is the function signature for a typed handler.
// P is the params type, R is the result type.
type HandlerFunc[P any, R any] func(ctx *Ctx, params P) (R, error)

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
