package runtime

import "net/http"

// HandleRPCForTest is an exported wrapper around handleRPC for in-process
// testing. Builds the dispatch registry from the package-level
// handlerRegistry (populated by RegisterFunction). Tests use
// ClearHandlerRegistry + RegisterFunction to populate, then call this from a
// project-built mux. Not intended for production use.
func HandleRPCForTest(w http.ResponseWriter, r *http.Request, cfg *Config) {
	registryMu.RLock()
	registry := make(map[string]registration, len(handlerRegistry))
	for _, reg := range handlerRegistry {
		registry[reg.method] = reg
	}
	registryMu.RUnlock()
	handleRPC(w, r, registry, cfg)
}

// SetJWTSecret sets the JWT secret on a config. Exported for testing.
func (c *Config) SetJWTSecret(secret string) {
	c.JWTSecret = secret
}

// SetPlatformURL sets the platform URL on a config. Exported for testing.
func (c *Config) SetPlatformURL(url string) {
	c.PlatformURL = url
}

// ApplyDefaults applies default values to the config. Exported for testing.
func (c *Config) ApplyDefaults() {
	c.withDefaults()
}
