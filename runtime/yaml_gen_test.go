package runtime

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// setupYAMLGenTest clears the handler registry before each test and restores
// the empty state on cleanup, so tests do not affect each other.
func setupYAMLGenTest(t *testing.T) {
	t.Helper()
	ClearHandlerRegistry()
	t.Cleanup(ClearHandlerRegistry)
}

// ---------------------------------------------------------------------------
// Generator round-trip tests
// ---------------------------------------------------------------------------

func TestGenerateHandlersYAML_empty_registry(t *testing.T) {
	setupYAMLGenTest(t)

	var buf bytes.Buffer
	err := GenerateHandlersYAML(&buf)
	require.NoError(t, err)

	out := buf.String()
	assert.Equal(t, "handlers:\n", out)
}

func TestGenerateHandlersYAML_minimal_handler(t *testing.T) {
	setupYAMLGenTest(t)

	RegisterHandler(Handler("sw.udfs.regex_match", func(_ *Ctx, p echoParams) (echoResult, error) {
		return echoResult{}, nil
	}))

	var buf bytes.Buffer
	require.NoError(t, GenerateHandlersYAML(&buf))

	// Must be valid YAML.
	var parsed struct {
		Handlers map[string]map[string]any `yaml:"handlers"`
	}
	require.NoError(t, yaml.Unmarshal(buf.Bytes(), &parsed))

	entry, ok := parsed.Handlers["sw.udfs.regex_match"]
	require.True(t, ok, "expected handler entry in output")
	assert.Equal(t, "jsonrpc", entry["type"])
	assert.Equal(t, "${param.handlers_url}/rpc", entry["url"])
}

func TestGenerateHandlersYAML_rich_metadata(t *testing.T) {
	setupYAMLGenTest(t)

	timeout := 15 * time.Second
	RegisterHandler(Handler("sw.actions.ban_user", func(_ *Ctx, p echoParams) (echoResult, error) {
		return echoResult{}, nil
	},
		Timeout(timeout),
		NameEN("Ban User"),
		NameRU("Заблокировать пользователя"),
		Retry(3, "exponential"),
		Async(),
	))

	var buf bytes.Buffer
	require.NoError(t, GenerateHandlersYAML(&buf))

	out := buf.String()

	// Must be valid YAML.
	var parsed struct {
		Handlers map[string]map[string]any `yaml:"handlers"`
	}
	require.NoError(t, yaml.Unmarshal([]byte(out), &parsed))

	entry := parsed.Handlers["sw.actions.ban_user"]
	assert.Equal(t, "15s", entry["timeout"])
	assert.Equal(t, true, entry["async"])

	retryMap, ok := entry["retry"].(map[string]any)
	require.True(t, ok, "retry should be a map")
	assert.Equal(t, 3, retryMap["max_attempts"])
	assert.Equal(t, "exponential", retryMap["backoff"])

	displayMap, ok := entry["display"].(map[string]any)
	require.True(t, ok, "display block should be a map")
	nameMap, ok := displayMap["name"].(map[string]any)
	require.True(t, ok, "name should be a map")
	assert.Equal(t, "Ban User", nameMap["en"])
	assert.Equal(t, "Заблокировать пользователя", nameMap["ru"])
}

func TestGenerateHandlersYAML_deterministic_alphabetical_order(t *testing.T) {
	setupYAMLGenTest(t)

	// Register in reverse alphabetical order; output should be sorted.
	for _, name := range []string{"z.handler", "m.handler", "a.handler"} {
		name := name
		RegisterHandler(Handler(name, func(_ *Ctx, p echoParams) (echoResult, error) {
			return echoResult{}, nil
		}))
	}

	var buf1, buf2 bytes.Buffer
	require.NoError(t, GenerateHandlersYAML(&buf1))
	require.NoError(t, GenerateHandlersYAML(&buf2))

	// Determinism: two runs produce identical bytes.
	assert.Equal(t, buf1.String(), buf2.String())

	// Order: "a.handler" line must appear before "m.handler" and "z.handler".
	out := buf1.String()
	posA := strings.Index(out, "a.handler")
	posM := strings.Index(out, "m.handler")
	posZ := strings.Index(out, "z.handler")
	assert.Less(t, posA, posM, "a.handler should precede m.handler")
	assert.Less(t, posM, posZ, "m.handler should precede z.handler")
}

func TestGenerateHandlersYAML_name_en_only(t *testing.T) {
	setupYAMLGenTest(t)

	RegisterHandler(Handler("sw.udfs.hash_partition", func(_ *Ctx, p echoParams) (echoResult, error) {
		return echoResult{}, nil
	}, NameEN("Hash Partition")))

	var buf bytes.Buffer
	require.NoError(t, GenerateHandlersYAML(&buf))

	var parsed struct {
		Handlers map[string]map[string]any `yaml:"handlers"`
	}
	require.NoError(t, yaml.Unmarshal(buf.Bytes(), &parsed))

	entry := parsed.Handlers["sw.udfs.hash_partition"]
	display, ok := entry["display"].(map[string]any)
	require.True(t, ok)
	name, ok := display["name"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "Hash Partition", name["en"])
	assert.Empty(t, name["ru"])
}

func TestRegisterHandler_panics_on_duplicate(t *testing.T) {
	setupYAMLGenTest(t)

	h := Handler("dup.method", func(_ *Ctx, p echoParams) (echoResult, error) {
		return echoResult{}, nil
	})
	RegisterHandler(h)
	assert.Panics(t, func() { RegisterHandler(h) }, "duplicate registration should panic")
}

// ---------------------------------------------------------------------------
// formatDuration tests
// ---------------------------------------------------------------------------

func TestFormatDuration(t *testing.T) {
	cases := []struct {
		name     string
		input    time.Duration
		expected string
	}{
		{"zero", 0, "0s"},
		{"seconds", 5 * time.Second, "5s"},
		{"thirty_seconds", 30 * time.Second, "30s"},
		{"one_minute", time.Minute, "1m"},
		{"ninety_seconds", 90 * time.Second, "1m30s"},
		{"one_hour", time.Hour, "1h"},
		{"two_hours", 2 * time.Hour, "2h"},
		{"complex", 2*time.Hour + 30*time.Minute + 15*time.Second, "2h30m15s"},
		{"sub_second_truncated", 500 * time.Millisecond, "0s"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expected, formatDuration(tc.input))
		})
	}
}

// ---------------------------------------------------------------------------
// Webhook-flag emission
// ---------------------------------------------------------------------------

// TestGenerateHandlersYAML_unauthenticated covers the simple bool flag.
func TestGenerateHandlersYAML_unauthenticated(t *testing.T) {
	setupYAMLGenTest(t)

	RegisterHandler(Handler("sw.webhook.stripe", func(_ *Ctx, p echoParams) (echoResult, error) {
		return echoResult{}, nil
	}, Unauthenticated()))

	var buf bytes.Buffer
	require.NoError(t, GenerateHandlersYAML(&buf))

	out := buf.String()
	assert.Contains(t, out, "unauthenticated: true")

	var parsed struct {
		Handlers map[string]map[string]any `yaml:"handlers"`
	}
	require.NoError(t, yaml.Unmarshal([]byte(out), &parsed))
	assert.Equal(t, true, parsed.Handlers["sw.webhook.stripe"]["unauthenticated"])
}

// TestGenerateHandlersYAML_webhook_full exercises the full webhook flag set
// the way a real Telegram-style action would declare itself.
func TestGenerateHandlersYAML_webhook_full(t *testing.T) {
	setupYAMLGenTest(t)

	RegisterHandler(Handler("community.webhook.telegram", func(_ *Ctx, p echoParams) (echoResult, error) {
		return echoResult{}, nil
	},
		Unauthenticated(),
		RawBodyAccess(),
		MaxBodyBytes(1<<20),
		RequestDedupKeyExpr("$payload.update_id"),
		TenantFromPayloadLookup(),
	))

	var buf bytes.Buffer
	require.NoError(t, GenerateHandlersYAML(&buf))

	out := buf.String()

	var parsed struct {
		Handlers map[string]map[string]any `yaml:"handlers"`
	}
	require.NoError(t, yaml.Unmarshal([]byte(out), &parsed))

	entry := parsed.Handlers["community.webhook.telegram"]
	require.NotNil(t, entry, "handler must be present")
	assert.Equal(t, true, entry["unauthenticated"])
	assert.Equal(t, true, entry["raw_body_access"])
	assert.Equal(t, 1<<20, entry["max_body_bytes"])

	dedup, ok := entry["request_dedup_key"].(map[string]any)
	require.True(t, ok, "request_dedup_key should be a map")
	assert.Equal(t, "expr", dedup["source"])
	assert.Equal(t, "$payload.update_id", dedup["expression"])

	tenantFrom, ok := entry["tenant_from"].(map[string]any)
	require.True(t, ok, "tenant_from should be a map")
	assert.Equal(t, "payload_lookup", tenantFrom["source"])
}

// TestGenerateHandlersYAML_dedup_param covers the param-source variant.
func TestGenerateHandlersYAML_dedup_param(t *testing.T) {
	setupYAMLGenTest(t)

	RegisterHandler(Handler("foo.bar", func(_ *Ctx, p echoParams) (echoResult, error) {
		return echoResult{}, nil
	}, RequestDedupKeyParam("idempotency_key")))

	var buf bytes.Buffer
	require.NoError(t, GenerateHandlersYAML(&buf))

	var parsed struct {
		Handlers map[string]map[string]any `yaml:"handlers"`
	}
	require.NoError(t, yaml.Unmarshal([]byte(buf.Bytes()), &parsed))

	dedup, ok := parsed.Handlers["foo.bar"]["request_dedup_key"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "param", dedup["source"])
	assert.Equal(t, "idempotency_key", dedup["param_name"])
}

// TestGenerateHandlersYAML_tenant_from_header covers the header variant.
func TestGenerateHandlersYAML_tenant_from_header(t *testing.T) {
	setupYAMLGenTest(t)

	RegisterHandler(Handler("foo.bar", func(_ *Ctx, p echoParams) (echoResult, error) {
		return echoResult{}, nil
	}, TenantFromHeader("X-Tenant-Code")))

	var buf bytes.Buffer
	require.NoError(t, GenerateHandlersYAML(&buf))

	var parsed struct {
		Handlers map[string]map[string]any `yaml:"handlers"`
	}
	require.NoError(t, yaml.Unmarshal([]byte(buf.Bytes()), &parsed))

	tf, ok := parsed.Handlers["foo.bar"]["tenant_from"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "header", tf["source"])
	assert.Equal(t, "X-Tenant-Code", tf["header_name"])
}
