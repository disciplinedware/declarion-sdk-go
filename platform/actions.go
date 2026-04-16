package platform

import (
	"context"
	"encoding/json"
	"fmt"
)

// ActionsClient wraps /api/actions/{code} endpoints.
type ActionsClient struct {
	c *Client
}

// InvokeParams configures an action invocation.
// Field names match core's actionRequest: ids, entity, args.
type InvokeParams struct {
	// IDs are the entity object IDs the action operates on (optional for global actions).
	IDs []string `json:"ids,omitempty"`
	// Entity is the entity scope (optional for global actions).
	Entity string `json:"entity,omitempty"`
	// Args are the handler parameters.
	Args map[string]any `json:"args,omitempty"`
}

// InvokeResult is the response from an action invocation.
type InvokeResult struct {
	Status           string `json:"status"`
	AuditOperationID string `json:"audit_operation_id,omitempty"`
	Result           any    `json:"result,omitempty"`
}

// Invoke calls POST /api/actions/{code}.
func (a *ActionsClient) Invoke(ctx context.Context, code string, params InvokeParams) (*InvokeResult, error) {
	body, status, err := a.c.do(ctx, "POST", fmt.Sprintf("/api/actions/%s", code), nil, params)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, &APIError{StatusCode: status, Body: string(body), Path: fmt.Sprintf("/api/actions/%s", code)}
	}
	var result InvokeResult
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("unmarshal action response: %w", err)
	}
	return &result, nil
}
