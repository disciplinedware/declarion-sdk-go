// Package errs is the one failure shape every Declarion path carries, in
// RFC 9457 form. A producer raises an occurrence; the party that answers a
// caller calls Render, which recomputes status, title and instance from the
// catalogue and discards whatever the producer put in them.
//
// Wire contract: public/docs/api/rest-api.md. Declaration:
// public/docs/build/dsl.md.
package errs

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
)

// ErrRetryable is the sentinel for "the same call can succeed later". An
// Error answers it from its declared retryability, so errors.Is and the wire
// member are one value read two ways.
var ErrRetryable = errors.New("retryable error")

// TypePrefix is what Code() strips. The published contract is the last
// segment, never the whole string, so this prefix can move without breaking
// a consumer.
const TypePrefix = "/errors/"

// TypeUnknown is RFC 9457's default for a failure with no declared identity.
const TypeUnknown = "about:blank"

// Codes this package raises itself. Every other code is written at its call
// site as the YAML declares it - a type is never renamed, so there is
// nothing for a constant to be rename-safe against.
const (
	// CodeInternalError is what an error matching no declared type renders
	// as, with an empty detail.
	CodeInternalError = "platform.internal_error"
	// CodeUndeclaredType titles an error whose own type no module declared,
	// leaving that type's identity intact.
	CodeUndeclaredType = "platform.undeclared_type"
	// CodeTooLarge replaces an object over the size bound. It is bounded by
	// construction: the offending type and nothing else.
	CodeTooLarge = "platform.error_too_large"
)

// FieldOffendingType names the type an over-bound object carried, truncated
// to OffendingTypeMaxBytes.
const FieldOffendingType = "offending_type"

// OffendingTypeMaxBytes bounds the one member CodeTooLarge carries, so the
// replacement can never itself exceed a bound.
const OffendingTypeMaxBytes = 200

// Error is any failure, on any path, in one shape.
//
// Status, Title and Instance are exported because every consumer must read
// them, and are NOT authoritative across a boundary: Render recomputes all
// three, so a producer that assigns them changes nothing on the wire.
type Error struct {
	Type      string `json:"type"`
	Status    int    `json:"status,omitempty"` // absent where the carrier has no HTTP status
	Title     string `json:"title"`
	Detail    string `json:"detail,omitempty"` // this occurrence; not localized
	Instance  string `json:"instance,omitempty"`
	Retryable bool   `json:"retryable"` // always emitted

	// Deny is the type's, stamped at the raise site from the catalogue the way
	// Retryable is, so a classifier downstream never has to consult one. Not
	// serialized: a caller learns it was refused from the type and the status,
	// and the member exists for the party RECORDING the decision.
	Deny bool `json:"-"`

	// Fields serialize as TOP-LEVEL members per RFC 9457, not a nested bag.
	Fields map[string]any `json:"-"`

	// Operator-only. Unexported so no call site can serialize it by
	// forgetting to.
	cause error
}

// Args carries the VALUES of the members a type declares. Nothing is ever
// substituted into a title.
type Args map[string]any

// New raises an occurrence of `code`, spelled as the YAML declares it.
//
// Retryable comes from the process catalogue when one is loaded, so a raise
// site can answer ErrRetryable before any boundary; a sidecar answering
// Declarion loads none and Declarion fills it as it renders. Status, title and
// instance describe a boundary and are filled only there - a raise site has no
// caller locale, no request, and no answer it is writing.
//
// Panics on a second Args: merging would make two maps mean one thing and
// discarding would make one mean nothing. The call-site gate catches it at
// build time.
func New(code string, args ...Args) *Error {
	if len(args) > 1 {
		panic("errs.New(" + code + "): at most one Args is legal")
	}
	e := &Error{Type: TypePrefix + code}
	if len(args) == 1 && len(args[0]) > 0 {
		e.Fields = make(map[string]any, len(args[0]))
		for k, v := range args[0] {
			e.Fields[k] = v
		}
	}
	if def, ok := catalogue().Lookup(code); ok {
		e.Retryable = def.Retryable
		e.Deny = def.Deny
	}
	return e
}

// Because attaches the operator's cause, which is never serialized.
func (e *Error) Because(err error) *Error {
	e.cause = err
	return e
}

// There is no WithDetail. Nothing in this platform writes a `detail`.
//
// A detail is not declared and not localized, and every sentence a person reads
// here is multilingual - every display name, label, enum value and screen
// title. A detail would be a permanent hole in that, and RFC 9457 is silent on
// language because it was written for APIs that have one. So a FACT goes in
// Fields, declared and typed, and a screen composes its own sentence around it
// in the reader's language; the SENTENCE is the type's title, one per type,
// translated; and what only an OPERATOR needs - a driver's message, a parse
// error, a library's text - is the cause, attached with Because, which is never
// serialized.
//
// The Detail FIELD stays, because an object arriving from a third party may
// carry one and must survive a round trip. Removing the method is the whole
// enforcement: a raise site that wants one no longer compiles, which is cheaper
// and stricter than any test.

// Error is the OPERATOR's string and carries the cause. Never write it to a
// wire; the wire carries Title and Detail, which a producer vetted.
//
// Nil-safe, like the three methods below it: this type travels as an `error`,
// and a nil pointer inside a non-nil interface is a shape errors.Is and
// errors.As walk into while looking for something else. Panicking there takes
// the process down on a path that has nothing to do with this error.
func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString(e.Code())
	switch {
	case e.Detail != "":
		b.WriteString(": ")
		b.WriteString(e.Detail)
	case e.Title != "":
		b.WriteString(": ")
		b.WriteString(e.Title)
	}
	if e.cause != nil {
		b.WriteString(": ")
		b.WriteString(e.cause.Error())
	}
	return b.String()
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *Error) Is(target error) bool {
	return e != nil && target == ErrRetryable && e.Retryable
}

// Code is the identifier a consumer compares, identical in every deployment.
// A method rather than a field so it cannot drift from Type.
func (e *Error) Code() string {
	if e == nil || e.Type == "" || e.Type == TypeUnknown {
		return ""
	}
	if i := strings.LastIndexByte(e.Type, '/'); i >= 0 {
		return e.Type[i+1:]
	}
	return e.Type
}

// IsNilError reports the trap Go has no syntax against: a nil *Error handed
// over as a non-nil `error`. Every caller holding the concrete type and
// returning it as `error` says "nothing failed", and the interface does not
// agree - read as a failure it records one that never happened, and every
// classifier below it dereferences a nil.
//
// The DIRECT dynamic type only. errors.As would also match a nil *Error found
// by unwrapping something non-nil, and a wrapper existing means something DID
// fail.
func IsNilError(err error) bool {
	e, ok := err.(*Error)
	return ok && e == nil
}

// IsDeny reports whether err was a caller turned away for lack of authority,
// which its TYPE declares. False for a plain error: an unrecognised failure is
// not evidence that someone was refused.
func IsDeny(err error) bool {
	e, ok := From(err)
	return ok && e.Deny
}

// HasCode reports whether err carries this declared type anywhere in its
// wrapped chain. The one way a consumer branches on identity.
func HasCode(err error, code string) bool {
	e, ok := From(err)
	return ok && e.Code() == code
}

func (e *Error) Ext(key string) (any, bool) {
	v, ok := e.Fields[key]
	return v, ok
}

// ExtInt reads an integer member across the forms JSON decoding produces, so
// no consumer writes this switch.
func (e *Error) ExtInt(key string) (int, bool) {
	v, ok := e.Fields[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			return 0, false
		}
		return int(i), true
	}
	return 0, false
}

func (e *Error) ExtString(key string) (string, bool) {
	s, ok := e.Fields[key].(string)
	return s, ok
}

// From walks the wrapped chain. False for a plain error and for nil, so a
// caller never reads an empty type as a successful lookup.
func From(err error) (*Error, bool) {
	var e *Error
	if errors.As(err, &e) && e != nil {
		return e, true
	}
	return nil, false
}

// A field named after one of these never overwrites it; the call-site gate
// rejects the declaration that would.
var knownMembers = map[string]bool{
	"type": true, "status": true, "title": true,
	"detail": true, "instance": true, "retryable": true,
}

// IsKnownMember reports a name RFC 9457 already gives the object. A declared
// member with one of these names is dropped rather than overwriting it, so a
// loader must refuse the declaration.
func IsKnownMember(name string) bool { return knownMembers[name] }

func (e *Error) MarshalJSON() ([]byte, error) {
	m := make(map[string]any, len(e.Fields)+6)
	for k, v := range e.Fields {
		if knownMembers[k] {
			continue
		}
		m[k] = v
	}
	m["type"] = e.Type
	// Status, title and instance describe a boundary, and a raised error has
	// none of them until Render fills them - which is what makes a STORED
	// occurrence the fact and never one caller's sentence.
	if e.Status != 0 {
		m["status"] = e.Status
	}
	if e.Title != "" {
		m["title"] = e.Title
	}
	if e.Detail != "" {
		m["detail"] = e.Detail
	}
	if e.Instance != "" {
		m["instance"] = e.Instance
	}
	m["retryable"] = e.Retryable
	return json.Marshal(m)
}

func (e *Error) UnmarshalJSON(b []byte) error {
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return err
	}
	*e = Error{}
	for k, v := range m {
		switch k {
		case "type":
			e.Type, _ = v.(string)
		case "status":
			// A status that is not a whole number is IGNORED, never fatal:
			// RFC 9457 §3.1 tells a consumer to ignore a member whose value is
			// not the form it expects, and rejecting the object over one
			// advisory number would throw away the type - the only member a
			// consumer branches on.
			if n, ok := v.(json.Number); ok {
				if i, err := n.Int64(); err == nil {
					e.Status = int(i)
				}
			}
		case "title":
			e.Title, _ = v.(string)
		case "detail":
			e.Detail, _ = v.(string)
		case "instance":
			e.Instance, _ = v.(string)
		case "retryable":
			e.Retryable, _ = v.(bool)
		default:
			if e.Fields == nil {
				e.Fields = make(map[string]any, len(m))
			}
			e.Fields[k] = v
		}
	}
	return nil
}

// <owner>.<name>: an owner is a module manifest name, hyphens verbatim.
var codePattern = regexp.MustCompile(`^[a-z][a-z0-9-]*\.[a-z][a-z0-9_]*$`)

func ValidCode(code string) bool { return codePattern.MatchString(code) }

// RFC 9457 Section 4: letter first, ALPHA/DIGIT/_, three characters or more.
var memberPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{2,}$`)

func ValidMemberName(name string) bool { return memberPattern.MatchString(name) }
