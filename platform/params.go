package platform

import (
	"context"
	"encoding/json"
	"fmt"
)

// ParamsClient wraps /api/params/{code} endpoints.
type ParamsClient struct {
	c *Client
}

// paramWrapper is the API response envelope: {"data": {"code": ..., "value": ...}}.
type paramWrapper struct {
	Data struct {
		Code  string `json:"code"`
		Value any    `json:"value"`
	} `json:"data"`
}

// Get retrieves a single parameter by code, returning the raw value.
// The platform resolves env var overrides server-side.
func (p *ParamsClient) Get(ctx context.Context, code string) (any, error) {
	path := fmt.Sprintf("/api/params/%s", code)
	body, status, err := p.c.do(ctx, "GET", path, nil, nil)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, &APIError{StatusCode: status, Body: string(body), Path: path}
	}
	var result paramWrapper
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("unmarshal param response: %w", err)
	}
	return result.Data.Value, nil
}

// GetParam retrieves a platform parameter and converts it to the requested type.
// Uses Go generics for type-safe parameter access. Accepts *ParamsClient + context.
//
//	token, err := platform.GetParam[string](ctx.Platform.Params(), ctx.Context, "clickup_api_token")
func GetParam[T any](p *ParamsClient, ctx context.Context, code string) (T, error) {
	var zero T
	raw, err := p.Get(ctx, code)
	if err != nil {
		return zero, err
	}
	if raw == nil {
		return zero, nil
	}

	// Try direct type assertion first (covers string, bool, float64 from JSON).
	if v, ok := raw.(T); ok {
		return v, nil
	}

	// Fall back to JSON round-trip for numeric conversions (JSON numbers are float64,
	// caller may want int, int64, etc.).
	b, err := json.Marshal(raw)
	if err != nil {
		return zero, fmt.Errorf("param %q: marshal for conversion: %w", code, err)
	}
	var result T
	if err := json.Unmarshal(b, &result); err != nil {
		return zero, fmt.Errorf("param %q: cannot convert to %T: %w", code, zero, err)
	}
	return result, nil
}

// Convert converts a raw value (typically a string from env var) to the target type.
// Uses JSON round-trip for type coercion.
func Convert[T any](raw any) (T, error) {
	var zero T
	if raw == nil {
		return zero, nil
	}
	if v, ok := raw.(T); ok {
		return v, nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return zero, fmt.Errorf("convert: marshal: %w", err)
	}
	var result T
	if err := json.Unmarshal(b, &result); err != nil {
		return zero, fmt.Errorf("convert to %T: %w", zero, err)
	}
	return result, nil
}
