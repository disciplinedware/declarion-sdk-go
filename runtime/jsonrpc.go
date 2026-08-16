package runtime

import (
	"encoding/json"

	"github.com/disciplinedware/declarion-sdk-go/errs"
)

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
	JSONRPC string    `json:"jsonrpc"`
	ID      string    `json:"id"`
	Result  any       `json:"result,omitempty"`
	Error   *ErrorObj `json:"error,omitempty"`
}

// ErrorObj is a JSON-RPC 2.0 error object.
type ErrorObj struct {
	Code int `json:"code"`
	// Message is what a person reads when nothing else is available. The
	// FACTS are in Data.
	Message string `json:"message"`
	// Data is the failure, in the one shape every Declarion path carries.
	// Declarion reads the type here and renders the caller's title from its own
	// declarations - which is why a sidecar needs no catalogue of its own.
	Data *errs.Error `json:"data,omitempty"`
}

// Standard JSON-RPC 2.0 error codes.
const (
	JSONRPCParseError     = -32700
	JSONRPCInvalidRequest = -32600
	JSONRPCMethodNotFound = -32601
	JSONRPCInvalidParams  = -32602
	JSONRPCInternalError  = -32603
	JSONRPCAppError       = -32000
)

// JSONRPCCodeFor derives the numeric JSON-RPC code from the failure's TYPE.
//
// Mechanical, and deliberately not a judgement: the number is a protocol
// artefact the transport wants, and the identity a consumer branches on is
// `data.type`. Only the three situations JSON-RPC itself reserves a number for
// get one; everything else is an application error.
func JSONRPCCodeFor(e *errs.Error) int {
	switch e.Code() {
	case "action.invalid_params", "platform.invalid_body_shape", "platform.bad_request":
		return JSONRPCInvalidParams
	case "handler.not_registered", "action.not_found":
		return JSONRPCMethodNotFound
	case "platform.invalid_body", "platform.read_body_failed", "platform.body_too_large":
		return JSONRPCParseError
	}
	return JSONRPCAppError
}

// NewErrorResponse answers with one failure object.
//
// The numeric JSON-RPC code is derived from the transport situation, not chosen
// for meaning: it is a protocol artefact, and the identity a consumer branches
// on is `data.type`. Message carries the object's own operator string so a
// reader with no structured parser still sees something.
func NewErrorResponse(id string, code int, e *errs.Error) *Response {
	e = errs.Bounded(e, 0)
	msg := ""
	if e != nil {
		msg = e.Error()
	}
	return &Response{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &ErrorObj{Code: code, Message: msg, Data: e},
	}
}

// NewResultResponse creates a success response.
func NewResultResponse(id string, result any) *Response {
	return &Response{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	}
}
