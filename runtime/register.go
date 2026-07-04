package runtime

import (
	"encoding/json"
	"sync"

	kern "github.com/disciplinedware/declarion-sdk-go/dispatch"
)

var (
	registryMu      sync.RWMutex
	handlerRegistry = kern.NewRegistry[*HandlerCtx]()
)

// RegisterHandler registers a Go handler for remote dispatch via Declarion.
// The Go registration records only the callable function and its reflected
// parameter schema. Actions, display, permissions, timeout, retry, webhook
// security, and other configuration live in hand-written YAML.
func RegisterHandler[P any, R any](code string, fn func(*HandlerCtx, P) (R, error)) {
	registryMu.Lock()
	defer registryMu.Unlock()
	kern.RegisterHandler[*HandlerCtx, P, R](handlerRegistry, code, fn)
}

func executeRegisteredHandler(code string, ctx *HandlerCtx, params json.RawMessage) (json.RawMessage, error) {
	registryMu.RLock()
	reg := handlerRegistry
	registryMu.RUnlock()
	return reg.Execute(code, ctx, params)
}

func registeredDeclarations() []kern.Declaration {
	registryMu.RLock()
	reg := handlerRegistry
	registryMu.RUnlock()
	return reg.Declarations()
}

func registeredHandlerCount() int {
	registryMu.RLock()
	reg := handlerRegistry
	registryMu.RUnlock()
	return reg.Len()
}

// ClearHandlerRegistry removes all registered handlers. Intended for test
// isolation only; production code never calls this.
func ClearHandlerRegistry() {
	registryMu.Lock()
	defer registryMu.Unlock()
	handlerRegistry = kern.NewRegistry[*HandlerCtx]()
}
