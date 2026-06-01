package runtime

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

// GenerateFunctionsYAML walks the process-wide handler registry (populated by
// RegisterFunction calls from init() functions) and emits a YAML document
// compatible with declarion-core's loader.
//
// The output contains two blocks:
//
//   - handlers:  one entry per registration (always).
//   - actions:   one entry per registration whose actionMeta is non-nil.
//
// Entries within each block are sorted alphabetically by method name so the
// output is stable across runs (required for deterministic git diffs and CI
// verify-generated checks).
//
// UDFs (registrations created with NoAction()) appear only under handlers:.
// Pure-compute handlers without UI exposure do not produce action entries.
//
// Type-mapping for params/result fields:
//
//   - string       -> type: string
//   - []string     -> type: array<string>
//   - int, int64   -> type: int
//   - float64      -> type: number
//   - bool         -> type: boolean
//   - time.Time    -> type: timestamp
//   - map[string]any, any -> type: json
func GenerateFunctionsYAML(out io.Writer) error {
	registryMu.RLock()
	sorted := make([]registration, len(handlerRegistry))
	copy(sorted, handlerRegistry)
	registryMu.RUnlock()
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].method < sorted[j].method })

	if _, err := fmt.Fprintln(out, "handlers:"); err != nil {
		return fmt.Errorf("write handlers header: %w", err)
	}
	for _, r := range sorted {
		if err := writeHandlerYAML(out, r); err != nil {
			return fmt.Errorf("write handler %s: %w", r.method, err)
		}
	}

	// actions: block — only registrations with actionMeta != nil.
	var withAction []registration
	for _, r := range sorted {
		if r.actionMeta != nil {
			withAction = append(withAction, r)
		}
	}
	if len(withAction) > 0 {
		if _, err := fmt.Fprintln(out, "actions:"); err != nil {
			return fmt.Errorf("write actions header: %w", err)
		}
		for _, r := range withAction {
			if err := writeActionYAML(out, r); err != nil {
				return fmt.Errorf("write action %s: %w", r.method, err)
			}
		}
	}

	return nil
}

// writeHandlerYAML emits one handler entry in YAML. Indentation is two spaces.
func writeHandlerYAML(out io.Writer, r registration) error {
	m := r.handlerMeta

	lines := []string{
		fmt.Sprintf("  %s:", r.method),
		"    type: jsonrpc",
		"    url: ${param.handlers_url}/rpc",
	}

	// Group A — dispatch fields.
	if m.Timeout != nil {
		lines = append(lines, fmt.Sprintf("    timeout: %s", formatDuration(*m.Timeout)))
	}
	if m.IsAsync {
		lines = append(lines, "    async: true")
	}
	if m.Idempotent {
		lines = append(lines, "    idempotent: true")
	}
	if m.Invoke != "" {
		lines = append(lines, fmt.Sprintf("    invoke: %s", m.Invoke))
	}
	if m.AllowNoObjects {
		lines = append(lines, "    allow_no_objects: true")
	}
	if m.ReadOnly {
		lines = append(lines, "    read_only: true")
	}
	if m.SuppressEvents {
		lines = append(lines, "    suppress_events: true")
	}
	if m.Audit != nil {
		lines = append(lines, fmt.Sprintf("    audit: %t", *m.Audit))
	}

	// Group B — webhook flags.
	if m.IsUnauthenticated {
		lines = append(lines, "    unauthenticated: true")
	}
	if m.HasRawBodyAccess {
		lines = append(lines, "    raw_body_access: true")
	}
	if m.MaxBodyBytes > 0 {
		lines = append(lines, fmt.Sprintf("    max_body_bytes: %d", m.MaxBodyBytes))
	}
	if m.RequestVerifier != "" {
		lines = append(lines, fmt.Sprintf("    request_verifier: %s", m.RequestVerifier))
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

	// Display name block — single source of truth lives on actionMeta. The
	// emitter mirrors it into the handler block when present so declarion's
	// handler-level display.name is populated for non-action consumers
	// (audit log entries, scheduler labels, etc.) without duplicating the
	// registration call.
	if r.actionMeta != nil {
		en, ru := r.actionMeta.Display.NameEN, r.actionMeta.Display.NameRU
		if en != "" || ru != "" {
			lines = append(lines, "    display:")
			lines = append(lines, formatDisplayName(en, ru))
		}
	}

	for _, line := range lines {
		if _, err := fmt.Fprintln(out, line); err != nil {
			return err
		}
	}
	return nil
}

// writeActionYAML emits one action entry in YAML. Indentation is two spaces.
func writeActionYAML(out io.Writer, r registration) error {
	a := r.actionMeta

	lines := []string{
		fmt.Sprintf("  %s:", r.method),
		fmt.Sprintf("    handler: %s", r.method),
	}

	if a.Display.NameEN != "" || a.Display.NameRU != "" || a.Display.Icon != "" {
		lines = append(lines, "    display:")
		if a.Display.NameEN != "" || a.Display.NameRU != "" {
			lines = append(lines, formatDisplayName(a.Display.NameEN, a.Display.NameRU))
		}
		if a.Display.Icon != "" {
			lines = append(lines, fmt.Sprintf("      icon: %s", a.Display.Icon))
		}
	}
	if a.Destructive {
		lines = append(lines, "    destructive: true")
	}
	if a.LongRunning {
		lines = append(lines, "    long_running: true")
	}
	if a.ProgressScreen != "" {
		lines = append(lines, fmt.Sprintf("    progress_screen: %s", a.ProgressScreen))
	}
	if a.RequiredPermission != "" {
		lines = append(lines, fmt.Sprintf("    required_permission: %s", a.RequiredPermission))
	}

	for _, line := range lines {
		if _, err := fmt.Fprintln(out, line); err != nil {
			return err
		}
	}
	return nil
}

// formatDisplayName builds a single-line "name: {en: ..., ru: ...}" entry
// nested under a display: block (six-space indentation).
func formatDisplayName(en, ru string) string {
	switch {
	case en != "" && ru != "":
		return fmt.Sprintf("      name: {en: %q, ru: %q}", en, ru)
	case en != "":
		return fmt.Sprintf("      name: {en: %q}", en)
	default:
		return fmt.Sprintf("      name: {ru: %q}", ru)
	}
}

// formatDuration converts a time.Duration to a human-readable string that
// declarion's YAML loader understands: "5s", "30s", "1m", "1m30s", "2h".
// Sub-second precision is not supported by the loader; values are truncated
// to seconds.
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
