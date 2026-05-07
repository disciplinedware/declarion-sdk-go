package runtime

import (
	"fmt"
	"time"
)

// HandlerOption configures a HandlerRegistration at registration time.
// The metadata is stored on the registration and consumed by the YAML generator;
// Serve does not read these fields.
type HandlerOption func(*HandlerMetadata)

// HandlerMetadata carries optional per-handler metadata attached at registration
// time. Populated by HandlerOption functions; read by GenerateHandlersYAML.
type HandlerMetadata struct {
	Timeout *time.Duration
	NameEN  string
	NameRU  string
	Retry   *RetryConfig
	IsAsync bool
}

// RetryConfig configures automatic retry behaviour for a handler.
type RetryConfig struct {
	// MaxAttempts is the total number of attempts (including the first).
	// Must be >= 2.
	MaxAttempts int
	// Backoff is the retry delay strategy. One of "exponential", "linear", "constant".
	Backoff string
}

// validBackoffs is the closed set of accepted backoff values.
var validBackoffs = map[string]bool{
	"exponential": true,
	"linear":      true,
	"constant":    true,
}

// Timeout sets the per-call deadline for the handler.
// Stored on the registration; emitted in generated YAML as the "timeout" field.
// Does not enforce a deadline at the Go call layer.
func Timeout(d time.Duration) HandlerOption {
	return func(m *HandlerMetadata) { m.Timeout = &d }
}

// NameEN sets the English display name. Emitted in generated YAML under
// display.name.en.
func NameEN(s string) HandlerOption {
	return func(m *HandlerMetadata) { m.NameEN = s }
}

// NameRU sets the Russian display name. Emitted in generated YAML under
// display.name.ru when non-empty.
func NameRU(s string) HandlerOption {
	return func(m *HandlerMetadata) { m.NameRU = s }
}

// Retry attaches a retry policy to the handler.
// maxAttempts must be >= 2.
// backoff must be one of "exponential", "linear", "constant"; panics otherwise.
func Retry(maxAttempts int, backoff string) HandlerOption {
	if maxAttempts < 2 {
		panic(fmt.Sprintf("runtime.Retry: maxAttempts must be >= 2, got %d", maxAttempts))
	}
	if !validBackoffs[backoff] {
		panic(fmt.Sprintf("runtime.Retry: backoff must be one of exponential|linear|constant, got %q", backoff))
	}
	return func(m *HandlerMetadata) {
		m.Retry = &RetryConfig{MaxAttempts: maxAttempts, Backoff: backoff}
	}
}

// Async marks the handler as async. Stored on the registration; emitted in
// generated YAML as async: true.
func Async() HandlerOption {
	return func(m *HandlerMetadata) { m.IsAsync = true }
}
