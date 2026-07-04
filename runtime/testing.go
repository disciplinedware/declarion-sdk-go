package runtime

import "net/http"

// HandleRPCForTest is an exported wrapper around handleRPC for in-process
// testing. Tests use ClearHandlerRegistry + RegisterHandler to populate the
// registry, then call this from a project-built mux. Not intended for production use.
func HandleRPCForTest(w http.ResponseWriter, r *http.Request, cfg *Config) {
	handleRPC(w, r, cfg)
}

// SetJWTSecret sets the JWT secret on a config. Exported for testing.
func (c *Config) SetJWTSecret(secret string) { c.JWTSecret = secret }

// SetPlatformURL sets the platform URL on a config. Exported for testing.
func (c *Config) SetPlatformURL(url string) { c.PlatformURL = url }

// ApplyDefaults applies default values to the config. Exported for testing.
func (c *Config) ApplyDefaults() { c.withDefaults() }
