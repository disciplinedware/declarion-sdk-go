package platform

import (
	"encoding/json"
	"mime"
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
	// TypeStreamInterrupted is a stream that ended without its terminal event.
	TypeStreamInterrupted = "transport.stream_interrupted"
	// TypeStreamUnreadable is a stream that arrived but did not follow the
	// contract: the wrong media type, a frame this client cannot parse, a
	// control or terminal event it does not know. ONE type for all of them,
	// because a consumer does the same thing with every one - the syntax that
	// broke is the operator's and stays the cause.
	TypeStreamUnreadable = "transport.stream_unreadable"
	// TypeStreamEventTooLarge is THIS client refusing an event over its own
	// bound, which is why it is not stream_unreadable: the peer did nothing
	// wrong and the consumer's move is to raise MaxEventBytes.
	TypeStreamEventTooLarge = "transport.stream_event_too_large"
)

// ProblemContentType is what Declarion answers a failure with. A body without
// it is not the platform speaking, whatever shape it happens to have.
const ProblemContentType = "application/problem+json"

// The client mints these itself, so they answer `retryable` in a process that
// loads no catalogue - which every standalone SDK consumer is. A transport
// failure IS retryable: nothing reached the platform, so nothing was decided.
var transportTypes = errs.Catalogue{
	TypeTransportFailed: {
		Status:    http.StatusBadGateway,
		Retryable: true,
		Fields:    map[string]string{FieldPath: "string"},
		Title:     errs.LocalizedString{"en": "The platform could not be reached."},
	},
	TypeUnreadableResponse: {
		Status:    http.StatusBadGateway,
		Retryable: true,
		Fields:    map[string]string{FieldStatus: "integer", FieldBody: "string", FieldPath: "string"},
		Title:     errs.LocalizedString{"en": "Something other than the platform answered this request."},
	},
	TypeStreamUnreadable: {
		Status: http.StatusBadGateway,
		// Not retryable: the peer answered, and it will answer the same way
		// again. A consumer retrying this spends the work twice for one result.
		Retryable: false,
		Fields:    map[string]string{FieldPath: "string"},
		Title:     errs.LocalizedString{"en": "The stream did not follow the contract this client reads."},
	},
	TypeStreamEventTooLarge: {
		Status:    http.StatusBadGateway,
		Retryable: false,
		Fields:    map[string]string{FieldLimitBytes: "integer", FieldPath: "string"},
		Title:     errs.LocalizedString{"en": "One event was larger than this client accepts."},
	},
	TypeStreamInterrupted: {
		Status:    http.StatusBadGateway,
		Retryable: true,
		Fields:    map[string]string{FieldPath: "string"},
		Title:     errs.LocalizedString{"en": "The stream ended before it finished."},
	},
}

// transportErr raises one of this client's OWN types with its declared
// retryability filled here rather than from a catalogue. errs.New reads the
// process catalogue, and a standalone consumer has none - so a caller asking
// errors.Is(err, errs.ErrRetryable) about a dial failure would be told no.
func transportErr(code string, args errs.Args, cause error) error {
	e := errs.New(code, args)
	if def, ok := transportTypes.Lookup(code); ok {
		e.Retryable = def.Retryable
		e.Status = def.Status
		if e.Title == "" {
			e.Title = def.TitleFor("en")
		}
	}
	return e.Because(cause)
}

// The members TypeUnreadableResponse carries.
//
// `peer_status`, never `status`: `status` is one of the six members RFC 9457
// gives the object, so serialization drops a declared member of that name and
// writes the type's own 502 instead - and StatusOf, reading the object back,
// answered 502 for a peer that had said 429.
const (
	FieldStatus = "peer_status"
	FieldBody   = "body"
	FieldPath   = "path"
	// The bound this client refused an event against, so a consumer reads the
	// number it has to raise rather than guessing it.
	FieldLimitBytes = "limit_bytes"
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
func errorFromResponse(status int, body []byte, path, contentType string) error {
	var e errs.Error
	// The MEDIA TYPE decides whether this is the platform speaking. Any proxy
	// may answer RFC 9457 - accepting a body for carrying a `type` would let a
	// proxy-owned identity pass as a platform one, and a consumer branch on it.
	if isProblemResponse(contentType) &&
		json.Unmarshal(body, &e) == nil && errs.ValidCode(e.Code()) && e.Type != errs.TypeUnknown {
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
	return transportErr(TypeUnreadableResponse, errs.Args{
		FieldStatus: status,
		FieldBody:   trimmed,
		FieldPath:   path,
	}, nil)
}

// isProblemResponse reports whether the body can be the platform's error object
// at all. The media type IS the trust boundary: Declarion answers a failure with
// `application/problem+json` and nothing else does.
//
// What it exists to catch is an INTERPOSER - a proxy, a load balancer, a gateway
// answering instead of the platform. Reading one of those as an error object is
// how a caller comes to believe the platform said something it never said, and
// `application/json` is exactly what an interposer sends. An absent header is
// one of them too.
//
// A deployment older than this contract answers `application/json`, and its
// failures reach a caller as `transport.unreadable_response` carrying the
// status and the body. That is deliberate: this platform carries no
// compatibility path, every consumer upgrades to the current version, and a
// second accepted media type is a permanent hole for one deployment nobody runs.
//
// Read without its parameters, so a `; charset=utf-8` suffix does not decide
// the question.
func isProblemResponse(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}
	return mediaType == ProblemContentType
}

// errorFromTransport names a request that never produced a response.
func errorFromTransport(path string, cause error) error {
	return transportErr(TypeTransportFailed, errs.Args{FieldPath: path}, cause)
}

// errorFromUnreadable names a response this client could not read as one.
func errorFromUnreadable(status int, path string, cause error) error {
	return transportErr(TypeUnreadableResponse, errs.Args{FieldStatus: status, FieldPath: path}, cause)
}

// errorFromInterruptedStream names a stream that ended without its terminal.
func errorFromInterruptedStream(path string, cause error) error {
	return transportErr(TypeStreamInterrupted, errs.Args{FieldPath: path}, cause)
}

// streamUnreadable names a stream that arrived and did not follow the contract.
// The syntax that broke is the operator's and stays the cause: a consumer does
// the same thing with every one of them, and a client that returned a plain Go
// error here left it reading text to find out what happened.
func streamUnreadable(path string, cause error) error {
	return transportErr(TypeStreamUnreadable, errs.Args{FieldPath: path}, cause)
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
