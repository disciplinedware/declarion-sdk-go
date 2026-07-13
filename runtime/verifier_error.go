package runtime

import "fmt"

// VerifierOutcome is the outcome class a verifier signals when it does not
// accept a request. It is the ONLY thing an anonymous caller learns: Declarion
// Core maps each class to one uniform public status and never copies the reason
// text to the provider.
//
//	VerifierRejected       -> 401. Credential or lookup failure. Every flavour of
//	                          it (unknown endpoint, wrong secret, no such bot)
//	                          MUST map here so the response cannot be used to
//	                          probe what exists.
//	VerifierInvalidRequest -> 400. Authenticated but structurally malformed
//	                          provider input.
//	VerifierUnavailable    -> 503. A platform/config problem, or a transient
//	                          state the provider should retry through (e.g. a
//	                          credential rotation that has not been promoted yet).
//	                          The provider retries; the request is not lost.
type VerifierOutcome string

const (
	VerifierRejected       VerifierOutcome = "rejected"
	VerifierInvalidRequest VerifierOutcome = "invalid_request"
	VerifierUnavailable    VerifierOutcome = "unavailable"
)

// Wire codes Core maps back to an outcome class (declarion_code on the JSON-RPC
// error envelope). They MUST match declarion-core engine.VerifierOutcomeCode*.
// Any other code - including a plain error a verifier returns by accident -
// classifies as unavailable on Core's side, which is the fail-safe direction: a
// misbehaving verifier is a platform problem, never a silent client rejection.
const (
	CodeVerifierRejected       = "VERIFIER_REJECTED"
	CodeVerifierInvalidRequest = "VERIFIER_INVALID_REQUEST"
	CodeVerifierUnavailable    = "VERIFIER_UNAVAILABLE"
)

// VerifierError is how a verifier declines a request. Reason is internal
// telemetry (logs, traces) and never reaches the caller.
type VerifierError struct {
	Outcome VerifierOutcome
	Reason  string
}

func (e *VerifierError) Error() string {
	return fmt.Sprintf("verifier outcome %s: %s", e.Outcome, e.Reason)
}

// appError renders the outcome onto the JSON-RPC error envelope Core decodes.
// An unknown/zero outcome renders as unavailable - a verifier that forgets to
// set the class must never fail open into an accepted-looking response.
func (e *VerifierError) appError() *AppError {
	code := CodeVerifierUnavailable
	rpc := JSONRPCInternalError
	switch e.Outcome {
	case VerifierRejected:
		code, rpc = CodeVerifierRejected, JSONRPCAppError
	case VerifierInvalidRequest:
		code, rpc = CodeVerifierInvalidRequest, JSONRPCInvalidParams
	}
	return &AppError{Code: rpc, Message: e.Reason, DeclarionCode: code}
}

// Reject declines a request as a credential/lookup failure (public 401).
func Reject(reason string) error {
	return &VerifierError{Outcome: VerifierRejected, Reason: reason}
}

// InvalidRequest declines an authenticated but malformed request (public 400).
func InvalidRequest(reason string) error {
	return &VerifierError{Outcome: VerifierInvalidRequest, Reason: reason}
}

// Unavailable declines a request as a platform/transient problem the provider
// should retry (public 503).
func Unavailable(format string, args ...any) error {
	return &VerifierError{Outcome: VerifierUnavailable, Reason: fmt.Sprintf(format, args...)}
}
