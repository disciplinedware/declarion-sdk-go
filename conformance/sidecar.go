package conformance

import (
	"fmt"

	"github.com/disciplinedware/declarion-sdk-go/runtime"
)

// ConformanceSidecarHandlers returns the handler registrations for the conformance
// test sidecar. Language-agnostic: any SDK that implements these three handlers
// can pass the conformance suite.
func ConformanceSidecarHandlers() []runtime.HandlerRegistration {
	return []runtime.HandlerRegistration{
		runtime.Handler("conformance.echo", handleEcho),
		runtime.Handler("conformance.error", handleError),
		runtime.Handler("conformance.callback", handleCallback),
	}
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

func handleEcho(ctx *runtime.Ctx, p echoParams) (echoResult, error) {
	return echoResult{
		Message:    fmt.Sprintf("hello %s", p.Name),
		TenantID:   ctx.TenantID,
		TenantCode: ctx.TenantCode,
		UserID:     ctx.UserID,
	}, nil
}

type errorParams struct{}

func handleError(ctx *runtime.Ctx, _ errorParams) (any, error) {
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

func handleCallback(ctx *runtime.Ctx, p callbackParams) (callbackResult, error) {
	if p.CallbackURL == "" {
		return callbackResult{}, &runtime.AppError{
			Code:          runtime.JSONRPCInvalidParams,
			Message:       "callback_url is required",
			DeclarionCode: runtime.CodeValidation,
		}
	}

	// Use ctx.Platform directly - it auto-attaches auth, traceparent, and baggage headers.
	records := []map[string]any{{"id": "test-1", "name": "conformance"}}
	_, err := ctx.Platform.Data().Create(ctx.Context, "test", records)
	if err != nil {
		return callbackResult{}, fmt.Errorf("callback failed: %w", err)
	}

	return callbackResult{CallbackStatus: 200}, nil
}
