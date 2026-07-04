package conformance

import (
	"fmt"

	"github.com/disciplinedware/declarion-sdk-go/runtime"
)

// RegisterConformanceSidecarHandlers registers the three handlers the
// conformance suite expects on the package-level handler registry. Tests
// call ClearHandlerRegistry first to isolate from any neighbouring registry
// state. Language-agnostic in spirit: any SDK that implements the three
// handlers below can pass the conformance suite.
func RegisterConformanceSidecarHandlers() {
	runtime.RegisterHandler[echoParams, echoResult]("conformance.echo", handleEcho)
	runtime.RegisterHandler[errorParams, any]("conformance.error", handleError)
	runtime.RegisterHandler[callbackParams, callbackResult]("conformance.callback", handleCallback)
}

// --- Handler implementations ---

type echoParams struct {
	Name string `json:"name"`
}

type echoResult struct {
	Message    string `json:"message"`
	TenantID   string `json:"tenant_id"`
	TenantCode string `json:"tenant_code"`
	UserID     string `json:"user_id"`
}

func handleEcho(ctx *runtime.HandlerCtx, p echoParams) (echoResult, error) {
	return echoResult{
		Message:    fmt.Sprintf("hello %s", p.Name),
		TenantID:   ctx.TenantID,
		TenantCode: ctx.TenantCode,
		UserID:     ctx.UserID,
	}, nil
}

type errorParams struct{}

func handleError(ctx *runtime.HandlerCtx, _ errorParams) (any, error) {
	return nil, &runtime.AppError{
		Code:          runtime.JSONRPCAppError,
		Message:       "conformance test error",
		DeclarionCode: runtime.CodeExternalService,
		Retryable:     true,
	}
}

type callbackParams struct {
	CallbackURL string `json:"callback_url"`
}

type callbackResult struct {
	CallbackStatus int `json:"callback_status"`
}

func handleCallback(ctx *runtime.HandlerCtx, p callbackParams) (callbackResult, error) {
	if p.CallbackURL == "" {
		return callbackResult{}, &runtime.AppError{
			Code:          runtime.JSONRPCInvalidParams,
			Message:       "callback_url is required",
			DeclarionCode: runtime.CodeValidation,
		}
	}

	// Use ctx.Platform directly - it auto-attaches auth, traceparent, and baggage headers.
	// BulkCreate dispatches one __create action per call (FLAT envelope:
	// {entity, fields}); the harness's fake platform accepts any path so
	// the call lands as a recorded callback regardless of the legacy
	// /api/data/{entity} -> /api/actions/{entity}.__create migration.
	fields := map[string]any{"id": "test-1", "name": "conformance"}
	_, err := ctx.Platform.Data().BulkCreate(ctx.Context, "test", fields)
	if err != nil {
		return callbackResult{}, fmt.Errorf("callback failed: %w", err)
	}

	return callbackResult{CallbackStatus: 200}, nil
}
