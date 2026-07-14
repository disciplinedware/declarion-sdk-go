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

// SetVerifierRunAs installs a run-as credential on a VerifierCtx built by hand in
// a test, so PlatformFor behaves exactly as it does in production (a client that
// reads in the named tenant under the run-as service user).
//
// Test support only. Production ctxs get this from the run-as header Core sends;
// verifier code never receives the credential itself, only its reach.
func SetVerifierRunAs(c *VerifierCtx, platformURL, runAsToken string) {
	c.platformURL = platformURL
	c.runAs = runAsToken
}
