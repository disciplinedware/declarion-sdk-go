package platform

import (
	"context"
	"encoding/json"
	"fmt"
)

// BatchOp is one action call inside a system.batch invocation. Mirrors the
// platform handler's BatchOp shape: {action, params}. Sync vs async is a
// property of the target action's registration, not of the op.
type BatchOp struct {
	Action string         `json:"action"`
	Params map[string]any `json:"params"`
}

// BatchOpResult is the per-op result returned by system.batch.
type BatchOpResult struct {
	Index     int    `json:"index"`
	OK        bool   `json:"ok"`
	Result    any    `json:"result,omitempty"`
	Error     string `json:"error,omitempty"`
	ErrorCode string `json:"error_code,omitempty"`
}

// BatchResponse is the wire-level result of system.batch. Committed=false
// means at least one op failed and the whole transaction rolled back; the
// failing op's index and error are in Results.
type BatchResponse struct {
	Results   []BatchOpResult `json:"results"`
	Committed bool            `json:"committed"`
}

// Batch is a fluent builder for the system.batch action. Use NewBatch to
// open one, .Call/.Upsert to add ops, and .Execute to send.
//
// The batch is sync + atomic by definition: Execute returns once the
// platform has committed (or rolled back) the entire transaction.
type Batch struct {
	c   *Client
	ops []BatchOp
}

// NewBatch starts a new system.batch builder. The returned builder is not
// safe for concurrent use; share results, not the builder.
func (c *Client) NewBatch() *Batch {
	return &Batch{c: c}
}

// Call adds one op to the batch. action is the registered action code
// (e.g. "data.upsert", "swiftward.actions.http_request"); params is the
// handler's params struct or a pre-built map[string]any.
//
// The builder marshals params via encoding/json so handler-typed param
// structs work without a separate map conversion step.
func (b *Batch) Call(action string, params any) *Batch {
	b.ops = append(b.ops, BatchOp{Action: action, Params: toParamsMap(params)})
	return b
}

// Upsert is sugar for Call("data.upsert", {entity, records, mode, unique_by}).
// Mirrors the data.upsert handler exactly; mode is one of
// "upsert" | "insert_if_missing" (the only two modes the platform
// currently supports). uniqueBy is the conflict key set; passing nil
// defaults to the entity's primary key.
func (b *Batch) Upsert(entity string, records []map[string]any, mode string, uniqueBy []string) *Batch {
	params := map[string]any{
		"entity":  entity,
		"records": records,
	}
	if mode != "" {
		params["mode"] = mode
	}
	if len(uniqueBy) > 0 {
		params["unique_by"] = uniqueBy
	}
	return b.Call("data.upsert", params)
}

// Execute sends the accumulated ops to POST /api/actions/system.batch.
// Returns the parsed BatchResponse. A logical op failure surfaces as
// committed=false with the failed-op index in Results, NOT as a Go error;
// Go error returns reflect transport / infra failures only.
func (b *Batch) Execute(ctx context.Context) (*BatchResponse, error) {
	if len(b.ops) == 0 {
		return nil, fmt.Errorf("declarion: batch.Execute called with no ops; add ops via .Call or .Upsert")
	}
	body := map[string]any{
		"actions": b.ops,
		"atomic":  true,
	}
	respBody, status, err := b.c.do(ctx, "POST", "/api/actions/system.batch", nil, body)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, &APIError{StatusCode: status, Body: string(respBody), Path: "/api/actions/system.batch"}
	}
	// The HTTP layer wraps handler returns under a "result" envelope.
	var envelope struct {
		Status string         `json:"status"`
		Result *BatchResponse `json:"result"`
	}
	if err := json.Unmarshal(respBody, &envelope); err != nil {
		return nil, fmt.Errorf("unmarshal batch response: %w", err)
	}
	if envelope.Result == nil {
		return nil, fmt.Errorf("batch response missing result body: %s", string(respBody))
	}
	return envelope.Result, nil
}

// toParamsMap converts an arbitrary value to map[string]any via JSON
// round-trip. Pre-built maps pass through cheaply via type assertion.
func toParamsMap(v any) map[string]any {
	if v == nil {
		return nil
	}
	if m, ok := v.(map[string]any); ok {
		return m
	}
	b, err := json.Marshal(v)
	if err != nil {
		// JSON marshal failure on a params struct is a programmer error;
		// surface it at Execute via an empty-params op rather than panicking
		// inside the builder.
		return map[string]any{}
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return map[string]any{}
	}
	return m
}
