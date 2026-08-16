package runtime

// HandlerFunc is the function signature for a typed handler.
// P is the params type, R is the result type.
//
// A handler reports a failure by returning an *errs.Error: the type it
// declares, its detail, and the members that type carries. Nothing else - no
// status, no title, no numeric code. Declarion fills the title from the
// declarations it loaded, in the caller's language, which is why a sidecar
// needs no catalogue of its own.
type HandlerFunc[P any, R any] func(ctx *HandlerCtx, params P) (R, error)
