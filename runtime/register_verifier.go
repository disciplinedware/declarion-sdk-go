package runtime

import (
	"sort"
	"sync"

	kern "github.com/disciplinedware/declarion-sdk-go/dispatch"
)

// VerifierFunc authenticates an external request and returns only facts
// Declarion may consume. It receives the closed request envelope via
// *VerifierCtx (exact raw body, named path values, allowlisted query/header
// values) and returns a VerifierResult. It must be READ-ONLY except bounded
// telemetry: it authenticates and resolves facts but never mutates provider or
// application state, burns tokens, or enqueues work.
type VerifierFunc func(*VerifierCtx) (VerifierResult, error)

// verifierRegistry is disjoint from handlerRegistry even when a code string
// collides: the validated token family selects the registry before method
// lookup, so a handler token can never reach a verifier and vice versa.
var (
	verifierRegistryMu sync.RWMutex
	verifierRegistry   = map[string]VerifierFunc{}
)

// RegisterVerifier registers an application verifier for remote dispatch over
// /rpc under the verifier-only token audience. A verifier has no typed params
// (the request envelope rides on VerifierCtx); its declaration is a type/url
// stub emitted into the generated `verifiers:` YAML. Panics on empty or
// duplicate code, mirroring RegisterHandler.
func RegisterVerifier(code string, fn VerifierFunc) {
	if code == "" {
		panic("declarion-sdk: RegisterVerifier with empty code")
	}
	if fn == nil {
		panic("declarion-sdk: RegisterVerifier with nil func for " + code)
	}
	verifierRegistryMu.Lock()
	defer verifierRegistryMu.Unlock()
	if _, dup := verifierRegistry[code]; dup {
		panic("declarion-sdk: duplicate verifier registration for " + code)
	}
	verifierRegistry[code] = fn
}

// lookupVerifier returns the registered verifier for a method, or (nil, false).
func lookupVerifier(code string) (VerifierFunc, bool) {
	verifierRegistryMu.RLock()
	defer verifierRegistryMu.RUnlock()
	fn, ok := verifierRegistry[code]
	return fn, ok
}

// registeredVerifierDeclarations returns the registered verifiers as
// kern.Declarations (code only - a verifier has no typed params), sorted, so
// YAML generation is driven the same way as handlers (writeVerifierYAML mirrors
// writeHandlerYAML).
func registeredVerifierDeclarations() []kern.Declaration {
	verifierRegistryMu.RLock()
	defer verifierRegistryMu.RUnlock()
	decls := make([]kern.Declaration, 0, len(verifierRegistry))
	for code := range verifierRegistry {
		decls = append(decls, kern.Declaration{Code: code})
	}
	sort.Slice(decls, func(i, j int) bool { return decls[i].Code < decls[j].Code })
	return decls
}

// registeredVerifierCount returns the number of registered verifiers.
func registeredVerifierCount() int {
	verifierRegistryMu.RLock()
	defer verifierRegistryMu.RUnlock()
	return len(verifierRegistry)
}

// ClearVerifierRegistry removes all registered verifiers. Test isolation only.
func ClearVerifierRegistry() {
	verifierRegistryMu.Lock()
	defer verifierRegistryMu.Unlock()
	verifierRegistry = map[string]VerifierFunc{}
}
