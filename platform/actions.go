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
//
// The request body sent to the platform is a flat JSON object: Args keys
// are merged at the top level, and IDs is promoted to the reserved "_ids"
// control key (required for single- and batch-scope actions). For
// entity-scoped actions, pass the fully qualified code (e.g.
// "lead.archive") as the Invoke `code` argument; there is no entity field.
type InvokeParams struct {
	// Args are the handler parameters (top-level keys in the JSON body).
	Args map[string]any
	// IDs are the object IDs for single/batch-scope actions. Forbidden
	// for global-scope actions.
	IDs []string
}

// InvokeResult is the response from an action invocation.
type InvokeResult struct {
	Status           string `json:"status"`
	AuditOperationID string `json:"audit_operation_id,omitempty"`
	Result           any    `json:"result,omitempty"`
}

// Invoke calls POST /api/actions/{code}. `code` must be fully qualified
// for entity-scoped actions (`entity.action`) or bare for globals.
func (a *ActionsClient) Invoke(ctx context.Context, code string, params InvokeParams) (*InvokeResult, error) {
	body := make(map[string]any, len(params.Args)+1)
	for k, v := range params.Args {
		body[k] = v
	}
	if params.IDs != nil {
		body["_ids"] = params.IDs
	}
	respBody, status, err := a.c.do(ctx, "POST", fmt.Sprintf("/api/actions/%s", code), nil, body)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, &APIError{StatusCode: status, Body: string(respBody), Path: fmt.Sprintf("/api/actions/%s", code)}
	}
	var result InvokeResult
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("unmarshal action response: %w", err)
	}
	return &result, nil
}
