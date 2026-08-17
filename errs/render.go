package errs

import "encoding/json"

// DefaultMaxBytes is the default of the `error_max_bytes` parameter. Stated
// here because the number is shared across repositories and two engineers
// choosing their own would produce a payload one side accepts and the other
// replaces.
const DefaultMaxBytes = 16384

// RenderContext is what a boundary knows and a producer does not.
type RenderContext struct {
	Catalogue     Catalogue
	Locale        string
	DefaultLocale string
	Instance      string
	MaxBytes      int // zero takes DefaultMaxBytes
}

// Render produces the object a caller receives, taking status, retryability
// and title from the declaration and discarding whatever the producer put in
// status, title and instance.
//
// An undeclared type keeps its own identity and takes the fallback: the
// title of platform.undeclared_type, status 500, retryable false, and an
// EMPTY detail, because nobody vetted that text.
func Render(e *Error, rc RenderContext) *Error {
	out := render(e, rc)
	if fits(out, rc.MaxBytes) {
		return out
	}
	return render(tooLarge(e), rc)
}

func render(e *Error, rc RenderContext) *Error {
	out := &Error{
		Type:      e.Type,
		Detail:    e.Detail,
		Instance:  rc.Instance,
		Retryable: e.Retryable,
		Deny:      e.Deny,
		cause:     e.cause,
	}
	if len(e.Fields) > 0 {
		out.Fields = make(map[string]any, len(e.Fields))
		for k, v := range e.Fields {
			out.Fields[k] = v
		}
	}
	if out.Type == "" {
		out.Type = TypeUnknown
	}
	if def, ok := rc.Catalogue.Lookup(e.Code()); ok {
		out.Status = def.Status
		out.Retryable = def.Retryable
		out.Deny = def.Deny
		out.Title = def.TitleFor(rc.Locale, rc.DefaultLocale)
		return out
	}
	out.Status = 500
	out.Retryable = false
	out.Detail = ""
	if def, ok := rc.Catalogue.Lookup(CodeUndeclaredType); ok {
		out.Title = def.TitleFor(rc.Locale, rc.DefaultLocale)
	}
	return out
}

// Bounded is the one validate-or-replace, called at every decode, render,
// persistence and emission boundary - a locally constructed oversized error
// never passes an ingress check.
//
// Over the bound is a REPLACEMENT: truncating a structured object produces
// something that parses and lies.
func Bounded(e *Error, maxBytes int) *Error {
	if e == nil || fits(e, maxBytes) {
		return e
	}
	return tooLarge(e)
}

func fits(e *Error, maxBytes int) bool {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	b, err := json.Marshal(e)
	if err != nil {
		return false
	}
	return len(b) <= maxBytes
}

// Bounded by construction rather than by re-checking. The original survives
// as the logged cause.
func tooLarge(e *Error) *Error {
	offending := e.Type
	if len(offending) > OffendingTypeMaxBytes {
		offending = offending[:OffendingTypeMaxBytes]
	}
	return &Error{
		Type:   TypePrefix + CodeTooLarge,
		Status: 500,
		Fields: map[string]any{FieldOffendingType: offending},
		cause:  e,
	}
}
