// Package errs is the one failure shape every Declarion path carries.
//
// The wire form is RFC 9457 "Problem Details for HTTP APIs": `type` is the
// primary identifier, `status` is advisory and absent where no HTTP status
// exists, and everything beyond the standard's five members is a declared
// field serialized at the top level. Six carriers transport it - buffered
// HTTP, the SSE terminal, JSON-RPC `error.data`, a batch item, an agent
// frame and the NDJSON terminal - and none of them models errors its own
// way.
//
// A producer raises an occurrence: the type, its safe detail, the values of
// the members its type declares, and an operator-only cause. The party that
// answers a caller renders it - Render recomputes `status`, `title` and
// `instance` from the loaded catalogue and discards whatever the producer
// put in them.
//
// See public/docs/api/rest-api.md for the wire contract and
// public/docs/build/dsl.md for the `errors:` declaration.
package errs

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
)

// ErrRetryable is the cross-package sentinel for "the same call can succeed
// later". An Error answers it from its declared retryability, so the Go
// classification and the wire member are one value read two ways.
var ErrRetryable = errors.New("retryable error")

// TypePrefix is the URI-reference prefix every declared type carries. The
// published contract is the last segment - Code() - not the whole string, so
// this prefix can move without breaking a consumer.
const TypePrefix = "/errors/"

// TypeUnknown is RFC 9457's default `type` for a failure with no declared
// identity.
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
// them; they are not authoritative across a boundary. Whoever answers a
// caller recomputes all three from the declaration, so a producer that
// assigns them changes nothing on the wire.
type Error struct {
	Type      string `json:"type"`               // "/errors/<code>"; the identifier
	Status    int    `json:"status,omitempty"`   // omitted when the carrier has no HTTP status
	Title     string `json:"title"`              // what to show a person, in the caller's language
	Detail    string `json:"detail,omitempty"`   // this occurrence; not localized
	Instance  string `json:"instance,omitempty"` // platform-minted, never a producer's
	Retryable bool   `json:"retryable"`          // always emitted; answered, never computed

	// Fields are the declared members of this type, serialized as top-level
	// members per RFC 9457 rather than as a nested bag.
	Fields map[string]any `json:"-"`

	// cause is the operator's error: logged, and unmarshalable by
	// construction so no later call site can serialize it by forgetting to.
	cause error
}

// Args carries the VALUES of the members a type declares. Nothing is ever
// substituted into a title; a member is structured data for whoever reads it.
type Args map[string]any

// New raises an occurrence of the declared type `code`, written exactly as
// the YAML declares it.
//
// Status and Retryable come from the process catalogue when one is loaded;
// a sidecar answering Declarion deliberately loads none and Declarion fills
// both as it renders. Title is never filled here - it needs a caller's
// locale, which a raise site does not have.
//
// At most one Args is legal. A second is a programming error the call-site
// gate rejects at build time; merging them would make two maps mean one
// thing and discarding one would make it mean nothing, so this panics.
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
		e.Status = def.Status
		e.Retryable = def.Retryable
	}
	return e
}

// Because attaches the operator's cause. It is logged and never serialized.
func (e *Error) Because(err error) *Error {
	e.cause = err
	return e
}

// Detail attaches a diagnostic line describing THIS occurrence. It exists
// for what neither the type nor its members can say, so writing one is a
// deliberate act rather than the default call.
func (e *Error) WithDetail(s string) *Error {
	e.Detail = s
	return e
}

// Error is the OPERATOR's string: it carries the cause. Never write it to a
// wire - the wire carries Title and Detail, both of which a producer vetted.
func (e *Error) Error() string {
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

// Unwrap reaches the operator's cause, so errors.Is and errors.As still
// answer for whatever the producer wrapped.
func (e *Error) Unwrap() error { return e.cause }

// Is answers the retryability sentinel from the declared value, so
// errors.Is(err, ErrRetryable) and the wire member are one fact.
func (e *Error) Is(target error) bool {
	return target == ErrRetryable && e.Retryable
}

// Code is the identifier a consumer compares: the last path segment of Type,
// identical in every deployment. It is a method rather than a second field so
// the identifier has exactly one stored home.
func (e *Error) Code() string {
	if e.Type == "" || e.Type == TypeUnknown {
		return ""
	}
	if i := strings.LastIndexByte(e.Type, '/'); i >= 0 {
		return e.Type[i+1:]
	}
	return e.Type
}

// Ext reads a declared member.
func (e *Error) Ext(key string) (any, bool) {
	v, ok := e.Fields[key]
	return v, ok
}

// ExtInt reads a declared member declared as an integer, across the forms
// JSON decoding produces. Every consumer would otherwise write this switch.
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

// ExtString reads a declared member declared as a string.
func (e *Error) ExtString(key string) (string, bool) {
	s, ok := e.Fields[key].(string)
	return s, ok
}

// From walks the wrapped chain for an Error. It answers false for a plain
// error and for nil, so a caller never reads an empty type as a successful
// lookup.
func From(err error) (*Error, bool) {
	var e *Error
	if errors.As(err, &e) && e != nil {
		return e, true
	}
	return nil, false
}

// knownMembers are the members Error stores in its own fields. A declared
// field carrying one of these names never overwrites them; the call-site
// gate rejects the declaration that would.
var knownMembers = map[string]bool{
	"type": true, "status": true, "title": true,
	"detail": true, "instance": true, "retryable": true,
}

func (e *Error) MarshalJSON() ([]byte, error) {
	m := make(map[string]any, len(e.Fields)+6)
	for k, v := range e.Fields {
		if knownMembers[k] {
			continue
		}
		m[k] = v
	}
	m["type"] = e.Type
	if e.Status != 0 {
		m["status"] = e.Status
	}
	m["title"] = e.Title
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
			if n, ok := v.(json.Number); ok {
				i, err := n.Int64()
				if err != nil {
					return err
				}
				e.Status = int(i)
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

// codePattern is con-one-spelling: <owner>.<name>, where <owner> is a module
// manifest name (hyphens included, verbatim) and <name> is lower_snake.
var codePattern = regexp.MustCompile(`^[a-z][a-z0-9-]*\.[a-z][a-z0-9_]*$`)

// ValidCode reports whether code is spelled the one way every error type is.
func ValidCode(code string) bool { return codePattern.MatchString(code) }

// memberPattern is RFC 9457 Section 4: start with a letter, ALPHA/DIGIT/`_`,
// three characters or longer.
var memberPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{2,}$`)

// ValidMemberName reports whether name is a legal declared-field name.
func ValidMemberName(name string) bool { return memberPattern.MatchString(name) }
