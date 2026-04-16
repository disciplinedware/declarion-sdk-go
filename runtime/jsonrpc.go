package runtime

import "encoding/json"

// JSON-RPC 2.0 envelope types.

// Request is a JSON-RPC 2.0 request envelope.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      string          `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// Response is a JSON-RPC 2.0 response envelope.
type Response struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      string      `json:"id"`
	Result  any         `json:"result,omitempty"`
	Error   *ErrorObj   `json:"error,omitempty"`
}

// ErrorObj is a JSON-RPC 2.0 error object.
type ErrorObj struct {
	Code    int        `json:"code"`
	Message string     `json:"message"`
	Data    *ErrorData `json:"data,omitempty"`
}

// ErrorData carries Declarion-specific error metadata.
type ErrorData struct {
	DeclarionCode string `json:"declarion_code,omitempty"`
	Retryable     bool   `json:"retryable,omitempty"`
}

// Canonical Declarion error codes.
const (
	CodeValidation       = "VALIDATION_ERROR"
	CodeExternalService  = "EXTERNAL_SERVICE_ERROR"
	CodeTimeout          = "TIMEOUT"
	CodeRateLimited      = "RATE_LIMITED"
	CodeNotFound         = "NOT_FOUND"
	CodePermissionDenied = "PERMISSION_DENIED"
	CodeProtocolMismatch = "PROTOCOL_VERSION_MISMATCH"
	CodeInternal         = "INTERNAL_ERROR"
)

// Standard JSON-RPC 2.0 error codes.
const (
	JSONRPCParseError     = -32700
	JSONRPCInvalidRequest = -32600
	JSONRPCMethodNotFound = -32601
	JSONRPCInvalidParams  = -32602
	JSONRPCInternalError  = -32603
	JSONRPCAppError       = -32000
)

// NewErrorResponse creates an error response.
func NewErrorResponse(id string, code int, message string, declarionCode string, retryable bool) *Response {
	resp := &Response{
		JSONRPC: "2.0",
		ID:      id,
		Error: &ErrorObj{
			Code:    code,
			Message: message,
		},
	}
	if declarionCode != "" || retryable {
		resp.Error.Data = &ErrorData{
			DeclarionCode: declarionCode,
			Retryable:     retryable,
		}
	}
	return resp
}

// NewResultResponse creates a success response.
func NewResultResponse(id string, result any) *Response {
	return &Response{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	}
}
