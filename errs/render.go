package errs

import "encoding/json"

// DefaultMaxBytes bounds a serialized error object. It is the DEFAULT of the
// `error_max_bytes` declared parameter, stated once here because the number
// is shared across repositories and two engineers choosing their own would
// produce a payload one side accepts and the other replaces.
const DefaultMaxBytes = 16384

// RenderContext is what a boundary knows and a producer does not.
type RenderContext struct {
	// Catalogue is the process catalogue this boundary renders from.
	Catalogue Catalogue
	// Locale is the caller's resolved locale.
	Locale string
	// DefaultLocale is the deployment fallback.
	DefaultLocale string
	// Instance identifies this occurrence to whoever answers for it. The
	// caller decides its form; a producer's value is always discarded.
	Instance string
	// MaxBytes bounds the serialized object. Zero takes DefaultMaxBytes.
	MaxBytes int
}

// Render produces the object a caller receives.
//
// It takes `status`, `retryable` and `title` from the declaration for this
// type and discards whatever the producer put in `status`, `title` and
// `instance` - the members a producer may read but never author. An error
// whose type no module declares keeps its own identity and takes the
// fallback rendering: the title of platform.undeclared_type, an EMPTY
// detail, status 500 and retryable false, because nobody vetted that text.
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

// Bounded is the one validate-or-replace: it answers e when the serialized
// object fits, and a bounded replacement when it does not. Called at every
// decode, render, persistence and emission boundary, because a locally
// constructed oversized error never passes an ingress check.
//
// Over the bound is a REPLACEMENT, never truncation in place: truncating a
// structured object produces something that parses and lies.
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

// tooLarge is bounded by construction rather than by re-checking: the
// offending type and nothing else, so the replacement can never itself
// exceed a bound. The original survives as the logged cause.
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
