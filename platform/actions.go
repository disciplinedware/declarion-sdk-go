package platform

import (
	"github.com/disciplinedware/declarion-sdk-go/errs"

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
// are merged at the top level, and IDs is promoted to the reserved
// "object_ids" control key (required for single- and batch-scope actions).
// For entity-scoped actions, pass the fully qualified code (e.g.
// "lead.archive") as the Invoke `code` argument; there is no entity field.
//
// The legacy `_ids` key was retired in declarion-core's 2026-06-01
// write-API rewrite and is now rejected by parseActionBody (400). Callers
// who pre-built bodies with `_ids` must rename to `object_ids`.
type InvokeParams struct {
	// Args are the handler parameters (top-level keys in the JSON body).
	Args map[string]any
	// IDs are the object IDs for single/batch-scope actions. Forbidden
	// for global-scope actions.
	IDs []string
	// TargetTenantID sends X-Declarion-Tenant-ID for this invocation.
	// Mutually exclusive with TargetTenantCode.
	TargetTenantID string
	// TargetTenantCode sends X-Declarion-Tenant-Code for this invocation.
	// Mutually exclusive with TargetTenantID.
	TargetTenantCode string
}

// InvokeResult is the response from an action invocation.
type InvokeResult struct {
	Status           string `json:"status"`
	AuditOperationID string `json:"audit_operation_id,omitempty"`
	Result           any    `json:"result,omitempty"`
	// Events is the action's declared synchronous chain, when it has one.
	// Absent means the action declares no such chain - never that a step
	// passed. A caller that waited for the chain reads this, not the status:
	// the parent committed, and an event is free to fail without undoing a
	// committed write, so "saved" and "saved, and the work behind it did not
	// run" are the same status and different reports.
	Events *EventsReport `json:"events,omitempty"`
}

// EventsReport is what the platform says the action's synchronous chain did.
type EventsReport struct {
	FailedCount   int `json:"failed_count,omitempty"`
	TimedOutCount int `json:"timed_out_count,omitempty"`
	// JobIDs names the rows behind asynchronous events, so a caller can read
	// their outcome later through the `job` entity - the same way it already
	// reads an asynchronous action's outcome from the `job_id` it was given.
	// Present only where the deployment turned it on.
	JobIDs []string `json:"job_ids,omitempty"`
	// Failures is in the handler's DECLARED step order, at every depth.
	Failures []EventFailure `json:"failures,omitempty"`
	// FailuresOmitted counts what the response bounds dropped. Non-zero means
	// there were more; silence here would read as "that is all of them".
	FailuresOmitted int `json:"failures_omitted,omitempty"`
}

// EventFailure is one declared step that did not do its work.
type EventFailure struct {
	Step    int    `json:"step"`
	Handler string `json:"handler"`
	// Object is the id a per-object step died on; NotAttempted counts the ones
	// after it that were never reached.
	Object       string        `json:"object,omitempty"`
	NotAttempted int           `json:"not_attempted,omitempty"`
	Error        *errs.Error   `json:"error,omitempty"`
	Events       *EventsReport `json:"events,omitempty"`
}

// Failed reports whether anything in this chain, at any depth, did not happen.
func (r *EventsReport) Failed() bool {
	return r != nil && (r.FailedCount > 0 || r.TimedOutCount > 0)
}

// Invoke calls POST /api/actions/{code}. `code` must be fully qualified
// for entity-scoped actions (`entity.action`) or bare for globals.
func (a *ActionsClient) Invoke(ctx context.Context, code string, params InvokeParams) (*InvokeResult, error) {
	body := make(map[string]any, len(params.Args)+1)
	for k, v := range params.Args {
		body[k] = v
	}
	if params.IDs != nil {
		body["object_ids"] = params.IDs
	}
	respBody, status, err := a.c.do(ctx, "POST", fmt.Sprintf("/api/actions/%s", code), nil, body, targetTenantOptions(params.TargetTenantID, params.TargetTenantCode)...)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, errorFromResponse(status, respBody, fmt.Sprintf("/api/actions/%s", code))
	}
	var result InvokeResult
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("unmarshal action response: %w", err)
	}
	return &result, nil
}
