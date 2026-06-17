package runtime

import (
	"encoding/json"
	"fmt"
	"sync"
)

// registration is the internal record stored per registered function. Combines
// handler dispatch metadata with optional action wrapper metadata. The YAML
// generator emits each registration's handlerMeta to the handlers: block, and
// (when actionMeta != nil) its actionMeta to the actions: block.
type registration struct {
	method      string
	dispatch    func(*Ctx, json.RawMessage) (any, error)
	handlerMeta HandlerMetadata
	actionMeta  *ActionMetadata

	// Group-D flags that influence final action wrapper routing.
	forceAction bool // Action() — generate action wrapper even with no group-C options
	noAction    bool // NoAction() — suppress action wrapper even if group-C options touched actionMeta
}

var (
	registryMu      sync.RWMutex
	handlerRegistry []registration
)

// RegisterFunction registers a Go function for remote dispatch via declarion.
// Always writes the handler entry to the process-wide registry. Populates the
// registration's actionMeta — read by GenerateFunctionsYAML when emitting the
// actions: block — iff any group-C UI option is supplied OR Action() is passed
// explicitly. NoAction() suppresses the action wrapper even when group-C
// options were provided (and panics if both NoAction() and any group-C option
// are present, since the intent is contradictory).
//
// Panics on duplicate method code. Tests can clear the registry via
// ClearHandlerRegistry.
//
// Used by every sidecar building atop declarion-sdk-go. See the project plan
// 2026-06-01-sdk-register-function-unified for design rationale.
func RegisterFunction[P any, R any](
	method string,
	fn func(*Ctx, P) (R, error),
	opts ...Option,
) {
	reg := registration{
		method:   method,
		dispatch: wrapDispatch[P, R](fn),
	}
	for _, o := range opts {
		o.apply(&reg)
	}

	// Routing of the action wrapper.
	if reg.noAction && (reg.actionMeta != nil || reg.forceAction) {
		conflicts := actionConflictNames(&reg)
		panic(fmt.Sprintf("runtime.RegisterFunction: method %q: NoAction() conflicts with %v — drop one", method, conflicts))
	}
	switch {
	case reg.noAction:
		reg.actionMeta = nil
	case reg.forceAction && reg.actionMeta == nil:
		// Forced action with no UI fields → bare action wrapper.
		reg.actionMeta = &ActionMetadata{}
	}

	registryMu.Lock()
	defer registryMu.Unlock()
	for _, existing := range handlerRegistry {
		if existing.method == method {
			panic(fmt.Sprintf("runtime.RegisterFunction: duplicate method %q", method))
		}
	}
	handlerRegistry = append(handlerRegistry, reg)
}

// wrapDispatch builds the type-erased dispatch closure that handleRPC calls.
// Unmarshals raw JSON params into the typed P, then invokes the typed handler.
func wrapDispatch[P any, R any](fn func(*Ctx, P) (R, error)) func(*Ctx, json.RawMessage) (any, error) {
	return func(ctx *Ctx, rawParams json.RawMessage) (any, error) {
		var params P
		if len(rawParams) > 0 {
			if err := json.Unmarshal(rawParams, &params); err != nil {
				return nil, &AppError{
					Code:          JSONRPCInvalidParams,
					Message:       fmt.Sprintf("invalid params: %s", err),
					DeclarionCode: CodeValidation,
				}
			}
		}
		return fn(ctx, params)
	}
}

// ClearHandlerRegistry removes all registered functions. Intended for test
// isolation only; production code never calls this.
func ClearHandlerRegistry() {
	registryMu.Lock()
	defer registryMu.Unlock()
	handlerRegistry = nil
}

// actionConflictNames lists the option groups that populated actionMeta or
// forced an action wrapper, for use in NoAction-conflict panic messages.
func actionConflictNames(r *registration) []string {
	var names []string
	if r.forceAction {
		names = append(names, "Action()")
	}
	if r.actionMeta != nil {
		if r.actionMeta.Display.NameEN != "" {
			names = append(names, "NameEN()")
		}
		if r.actionMeta.Display.NameRU != "" {
			names = append(names, "NameRU()")
		}
		if r.actionMeta.Display.Icon != "" {
			names = append(names, "Icon()")
		}
		if r.actionMeta.Destructive {
			names = append(names, "Destructive()")
		}
		if r.actionMeta.LongRunning {
			names = append(names, "LongRunning()")
		}
		if r.actionMeta.ProgressScreen != "" {
			names = append(names, "ProgressScreen()")
		}
		if r.actionMeta.RequiredPermission != "" {
			names = append(names, "RequiredPermission()")
		}
		if r.actionMeta.Internal {
			names = append(names, "Internal()")
		}
	}
	if len(names) == 0 {
		names = []string{"<unknown action option>"}
	}
	return names
}
