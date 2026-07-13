package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"go.uber.org/zap"

	"github.com/disciplinedware/declarion-sdk-go/platform"
)

// VerifierCtx is the execution context of an external-request verifier. It
// exposes the closed request envelope (exact raw body, named path values, and
// allowlisted query/header values) read-only, plus an optional Platform client.
//
// Platform is non-nil ONLY when the verifier declares a run-as service user and
// Core minted a run-as credential for the call - it is NEVER constructed from
// the powerless verifier call token. Without a run-as credential the verifier
// uses only its own injected dependencies (Platform is nil).
type VerifierCtx struct {
	// Context is the underlying Go context with cancellation/deadline.
	Context context.Context

	// Logger is a structured zap logger pre-tagged with the verifier method.
	Logger *zap.Logger

	// ActionCode is the external action the request targets.
	ActionCode string

	// VerifierCode is this verifier's registry code.
	VerifierCode string

	// HTTPMethod is the inbound HTTP method (e.g. POST).
	HTTPMethod string

	// Path is the normalized request path (the external action's route suffix).
	Path string

	// PathValues are the named path captures declared in external_request.path.
	PathValues map[string]string

	// Query holds allowlisted query values as ordered string lists.
	Query map[string][]string

	// Headers holds allowlisted header values as ordered string lists (keys
	// lowercased).
	Headers map[string][]string

	// RawBody is the exact request body bytes. Provider parsing belongs to the
	// verifier - use DecodeJSON or parse RawBody directly.
	RawBody []byte

	// RequestID correlates the verifier call with the originating request.
	RequestID string

	// RemoteAddress is the trusted-proxy-normalized client address.
	RemoteAddress string

	// Platform is a typed Declarion platform client, non-nil ONLY when a run-as
	// credential was supplied. Verifiers use it for platform reads (e.g. secret
	// reveal) under the declared run-as service user's grants.
	Platform *platform.Client
}

// DecodeJSON unmarshals the exact raw body into v. Returns an error when the
// body is empty or not valid JSON for the target shape.
func (c *VerifierCtx) DecodeJSON(v any) error {
	if len(c.RawBody) == 0 {
		return fmt.Errorf("verifier: empty request body")
	}
	return json.Unmarshal(c.RawBody, v)
}

// Header returns the first allowlisted value for a header name (case-insensitive),
// or "" when absent. Header keys in the envelope are lowercased.
func (c *VerifierCtx) Header(name string) string {
	return firstValue(c.Headers, strings.ToLower(name))
}

// QueryValue returns the first allowlisted value for a query name, or "".
func (c *VerifierCtx) QueryValue(name string) string {
	return firstValue(c.Query, name)
}

func firstValue(m map[string][]string, key string) string {
	if vs, ok := m[key]; ok && len(vs) > 0 {
		return vs[0]
	}
	return ""
}
