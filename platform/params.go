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

// paramLookupResponse mirrors the platform's wire envelope for
// GET /api/params/{code}: HTTP 200 always, with {found, value?, source?}.
// "found:false" means the parameter is undeclared, restricted-category,
// or has no value at any resolution layer - the SDK substitutes the
// caller's default. The default never crosses the wire.
type paramLookupResponse struct {
	Data struct {
		Code   string `json:"code"`
		Found  bool   `json:"found"`
		Value  any    `json:"value,omitempty"`
		Source string `json:"source,omitempty"`
		Type   string `json:"type,omitempty"`
	} `json:"data"`
}

// Lookup fetches a parameter and reports whether the platform has a value
// for it. found=false means the SDK caller should use its default;
// found=true means the value is authoritative (even if it is nil/zero).
// Transport errors propagate; "not found" is never an error.
func (p *ParamsClient) Lookup(ctx context.Context, code string) (value any, found bool, source string, err error) {
	path := fmt.Sprintf("/api/params/%s", code)
	body, status, ferr := p.c.do(ctx, "GET", path, nil, nil)
	if ferr != nil {
		return nil, false, "", ferr
	}
	if status < 200 || status >= 300 {
		return nil, false, "", &APIError{StatusCode: status, Body: string(body), Path: path}
	}
	var result paramLookupResponse
	if jerr := json.Unmarshal(body, &result); jerr != nil {
		return nil, false, "", fmt.Errorf("unmarshal param response: %w", jerr)
	}
	return result.Data.Value, result.Data.Found, result.Data.Source, nil
}

// GetParam retrieves a platform parameter and coerces it to T.
//
// Contract:
//   - Platform has a value     -> (value, nil). Explicit nil is honoured
//     (zero T for non-pointer T, nil pointer for pointer T).
//   - Platform reports not-found -> (def, nil). The default is used as-is;
//     no error. Devs may call GetParam with new param codes before they
//     are declared in YAML; that is intentional.
//   - Coercion failure          -> (zero T, error). Indicates a programming
//     bug (T mismatches stored type).
//   - Transport / API failure   -> (zero T, err). Caller MUST propagate.
//     Defaults are for absent values; broken infrastructure is an error.
//
// The default `def` never crosses the wire. The server doesn't track caller
// defaults, so two callers with different defaults always see consistent
// server state.
//
//	token, err := platform.GetParam[string](p, ctx, "clickup_api_token", "")
//	maxRetries, err := platform.GetParam[int](p, ctx, "max_retries", 3)
//	enabled, err := platform.GetParam[bool](p, ctx, "feature_flag", false)
func GetParam[T any](p *ParamsClient, ctx context.Context, code string, def T) (T, error) {
	value, found, _, err := p.Lookup(ctx, code)
	if err != nil {
		var zero T
		return zero, err
	}
	if !found {
		return def, nil
	}

	// Direct type assertion fast path.
	if v, ok := value.(T); ok {
		return v, nil
	}

	// JSON round-trip handles numeric widths (JSON numbers arrive as
	// float64), struct shapes, multilang/locale maps, and explicit
	// nil values (json.Unmarshal of `null` -> zero T / nil pointer).
	b, merr := json.Marshal(value)
	if merr != nil {
		var zero T
		return zero, fmt.Errorf("param %q: marshal for conversion: %w", code, merr)
	}
	var result T
	if uerr := json.Unmarshal(b, &result); uerr != nil {
		var zero T
		return zero, fmt.Errorf("param %q: cannot convert stored %T to %T: %w", code, value, result, uerr)
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
