package runtime

import "net/http"

// HandleRPCForTest is an exported wrapper around handleRPC for in-process testing.
// Not intended for production use.
func HandleRPCForTest(w http.ResponseWriter, r *http.Request, registry map[string]HandlerRegistration, cfg *Config) {
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
