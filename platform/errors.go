package platform

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/disciplinedware/declarion-sdk-go/errs"
)

// The types this client mints itself, for a failure that is NOT Declarion's
// answer: nothing reached the platform, or what came back was not the platform
// speaking. Declarion's own failures arrive as error objects and keep their own
// types.
const (
	// TypeTransportFailed is the request never completing - dial, TLS, a reset
	// connection, a deadline on this side.
	TypeTransportFailed = "transport.request_failed"
	// TypeUnreadableResponse is a non-2xx whose body is not an error object:
	// an ingress 502, a proxy's HTML, an empty body. It carries `status` and a
	// bounded `body` so a person can see what actually answered.
	TypeUnreadableResponse = "transport.unreadable_response"
)

// FieldStatus and FieldBody are the members TypeUnreadableResponse carries.
const (
	FieldStatus = "status"
	FieldBody   = "body"
	FieldPath   = "path"
)

// unreadableBodyMaxBytes bounds what an unreadable body contributes. Not a
// truncation of a structured object - this body is by definition not one - but
// a bound on prose from something that is not the platform.
const unreadableBodyMaxBytes = 2048

// errorFromResponse turns a non-2xx into an error, in ONE place.
//
// Declarion answers `application/problem+json` and the object it sends IS the
// error, types and members intact - re-classifying here would erase the
// identity the whole contract exists to preserve. Anything else came from
// something that is not Declarion, and gets a `transport.*` type of ours with
// the status and the bounded body attached, because "HTTP 502" from an ingress
// is a different fact from a platform failure and must not wear its clothes.
func errorFromResponse(status int, body []byte, path string) error {
	var e errs.Error
	if json.Unmarshal(body, &e) == nil && e.Type != "" && e.Type != errs.TypeUnknown {
		// The type and every member are the platform's, untouched. Only an
		// ABSENT status is filled from the response: the object claims to carry
		// one, and a caller reading zero would branch on a status nothing had.
		if e.Status == 0 {
			e.Status = status
		}
		return errs.Bounded(&e, 0)
	}
	trimmed := strings.TrimSpace(string(body))
	if len(trimmed) > unreadableBodyMaxBytes {
		trimmed = trimmed[:unreadableBodyMaxBytes]
	}
	return errs.New(TypeUnreadableResponse, errs.Args{
		FieldStatus: status,
		FieldBody:   trimmed,
		FieldPath:   path,
	})
}

// errorFromTransport names a request that never produced a response.
func errorFromTransport(path string, cause error) error {
	return errs.New(TypeTransportFailed, errs.Args{FieldPath: path}).Because(cause)
}

// StatusOf reads the HTTP status a failure carried, for a caller that must
// branch on one. False when the failure is not an unreadable response - a
// Declarion error carries its own `status`, which Render put there.
func StatusOf(err error) (int, bool) {
	e, ok := errs.From(err)
	if !ok {
		return 0, false
	}
	if n, ok := e.ExtInt(FieldStatus); ok {
		return n, true
	}
	if e.Status != 0 {
		return e.Status, true
	}
	return 0, false
}

// IsNotFound reports whether the platform answered "no such thing", by TYPE
// where it said so and by status otherwise.
func IsNotFound(err error) bool {
	if e, ok := errs.From(err); ok {
		switch e.Code() {
		case "entity.not_found", "action.not_found", "platform.route_not_found":
			return true
		}
	}
	status, ok := StatusOf(err)
	return ok && status == http.StatusNotFound
}
