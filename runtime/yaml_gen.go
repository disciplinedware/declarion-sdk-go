package runtime

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

// handlerRegistry is the process-wide registry populated by RegisterHandler.
// It is keyed by method name and used by GenerateHandlersYAML.
var handlerRegistry []HandlerRegistration

// RegisterHandler adds a HandlerRegistration to the process-wide registry so
// that GenerateHandlersYAML can discover it. Call from init() in each package
// that defines handlers. Panics on duplicate method names.
//
// Consumers that drive generation via cmd/gen-handlers-yaml import their handler
// packages as side-effects (blank imports), which triggers init() calls that call
// RegisterHandler. The generator then calls GenerateHandlersYAML.
func RegisterHandler(h HandlerRegistration) {
	for _, existing := range handlerRegistry {
		if existing.Method == h.Method {
			panic(fmt.Sprintf("runtime.RegisterHandler: duplicate method %q", h.Method))
		}
	}
	handlerRegistry = append(handlerRegistry, h)
}

// ClearHandlerRegistry removes all registered handlers. Intended for test
// isolation only; production code never calls this.
func ClearHandlerRegistry() {
	handlerRegistry = nil
}

// GenerateHandlersYAML walks the process-wide handler registry (populated by
// RegisterHandler calls from init() functions) and emits a YAML document
// compatible with declarion-core's handler schema loader.
//
// The output is a "handlers:" block with one entry per registered handler.
// Entries are sorted alphabetically by method name so the output is stable
// across runs (required for deterministic git diffs and CI verify-generated checks).
//
// Type-mapping for params/result fields:
//   - string       -> type: string
//   - []string     -> type: array<string>
//   - int, int64   -> type: int
//   - float64      -> type: number
//   - bool         -> type: boolean
//   - time.Time    -> type: timestamp
//   - map[string]any, any -> type: json
//
// For consumers that use per-handler metadata (Timeout, NameEN/RU, Retry, Async)
// the corresponding YAML fields are emitted when present.
func GenerateHandlersYAML(out io.Writer) error {
	// Sort for stable output.
	sorted := make([]HandlerRegistration, len(handlerRegistry))
	copy(sorted, handlerRegistry)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Method < sorted[j].Method
	})

	_, err := fmt.Fprintln(out, "handlers:")
	if err != nil {
		return fmt.Errorf("write handlers header: %w", err)
	}

	for _, h := range sorted {
		if err := writeHandlerYAML(out, h); err != nil {
			return fmt.Errorf("write handler %s: %w", h.Method, err)
		}
	}

	return nil
}

// writeHandlerYAML emits one handler entry in YAML. Indentation is two spaces.
func writeHandlerYAML(out io.Writer, h HandlerRegistration) error {
	m := h.Metadata

	lines := []string{
		fmt.Sprintf("  %s:", h.Method),
		"    type: jsonrpc",
		"    url: ${param.handlers_url}/rpc",
	}

	// Timeout: emit formatted duration.
	if m.Timeout != nil {
		lines = append(lines, fmt.Sprintf("    timeout: %s", formatDuration(*m.Timeout)))
	}

	// Async.
	if m.IsAsync {
		lines = append(lines, "    async: true")
	}

	// Webhook-action flags.
	if m.IsUnauthenticated {
		lines = append(lines, "    unauthenticated: true")
	}
	if m.HasRawBodyAccess {
		lines = append(lines, "    raw_body_access: true")
	}
	if m.MaxBodyBytes > 0 {
		lines = append(lines, fmt.Sprintf("    max_body_bytes: %d", m.MaxBodyBytes))
	}
	if m.RequestDedupKey != nil {
		lines = append(lines, "    request_dedup_key:")
		lines = append(lines, fmt.Sprintf("      source: %s", m.RequestDedupKey.Source))
		if m.RequestDedupKey.ParamName != "" {
			lines = append(lines, fmt.Sprintf("      param_name: %s", m.RequestDedupKey.ParamName))
		}
		if m.RequestDedupKey.Expression != "" {
			lines = append(lines, fmt.Sprintf("      expression: %q", m.RequestDedupKey.Expression))
		}
		if m.RequestDedupKey.RequiredForMutating {
			lines = append(lines, "      required_for_mutating: true")
		}
	}
	if m.TenantFrom != nil {
		lines = append(lines, "    tenant_from:")
		lines = append(lines, fmt.Sprintf("      source: %s", m.TenantFrom.Source))
		if m.TenantFrom.HeaderName != "" {
			lines = append(lines, fmt.Sprintf("      header_name: %s", m.TenantFrom.HeaderName))
		}
	}

	// Retry block.
	if m.Retry != nil {
		lines = append(lines,
			"    retry:",
			fmt.Sprintf("      max_attempts: %d", m.Retry.MaxAttempts),
			fmt.Sprintf("      backoff: %s", m.Retry.Backoff),
		)
	}

	// Display name block.
	if m.NameEN != "" || m.NameRU != "" {
		lines = append(lines, "    display:")
		if m.NameEN != "" && m.NameRU != "" {
			lines = append(lines, fmt.Sprintf("      name: {en: %q, ru: %q}", m.NameEN, m.NameRU))
		} else if m.NameEN != "" {
			lines = append(lines, fmt.Sprintf("      name: {en: %q}", m.NameEN))
		} else {
			lines = append(lines, fmt.Sprintf("      name: {ru: %q}", m.NameRU))
		}
	}

	for _, line := range lines {
		if _, err := fmt.Fprintln(out, line); err != nil {
			return err
		}
	}
	return nil
}

// formatDuration converts a time.Duration to a human-readable string that
// declarion's YAML loader understands: "5s", "30s", "1m", "1m30s", "2h".
// Sub-second precision is not supported by the loader; values are truncated to seconds.
func formatDuration(d time.Duration) string {
	d = d.Truncate(time.Second)
	if d == 0 {
		return "0s"
	}
	h := d / time.Hour
	d -= h * time.Hour
	m := d / time.Minute
	d -= m * time.Minute
	s := d / time.Second

	var b strings.Builder
	if h > 0 {
		fmt.Fprintf(&b, "%dh", h)
	}
	if m > 0 {
		fmt.Fprintf(&b, "%dm", m)
	}
	if s > 0 {
		fmt.Fprintf(&b, "%ds", s)
	}
	return b.String()
}
