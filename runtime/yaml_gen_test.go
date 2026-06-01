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

// registerEchoFn is a small helper that registers a bare handler with no
// options. Mirrors the legacy "Handler(method, fn)" sugar.
func registerEchoFn(method string, opts ...Option) {
	RegisterFunction[echoParams, echoResult](method, func(_ *Ctx, _ echoParams) (echoResult, error) {
		return echoResult{}, nil
	}, opts...)
}

// ---------------------------------------------------------------------------
// Generator round-trip tests
// ---------------------------------------------------------------------------

func TestGenerateFunctionsYAML_empty_registry(t *testing.T) {
	setupYAMLGenTest(t)

	var buf bytes.Buffer
	err := GenerateFunctionsYAML(&buf)
	require.NoError(t, err)

	out := buf.String()
	assert.Equal(t, "handlers:\n", out)
}

func TestGenerateFunctionsYAML_minimal_handler(t *testing.T) {
	setupYAMLGenTest(t)

	registerEchoFn("sw.udfs.regex_match", NoAction())

	var buf bytes.Buffer
	require.NoError(t, GenerateFunctionsYAML(&buf))

	var parsed struct {
		Handlers map[string]map[string]any `yaml:"handlers"`
		Actions  map[string]map[string]any `yaml:"actions"`
	}
	require.NoError(t, yaml.Unmarshal(buf.Bytes(), &parsed))

	entry, ok := parsed.Handlers["sw.udfs.regex_match"]
	require.True(t, ok, "expected handler entry in output")
	assert.Equal(t, "jsonrpc", entry["type"])
	assert.Equal(t, "${param.handlers_url}/rpc", entry["url"])

	// NoAction() suppresses any action: block. The whole map should be absent.
	assert.Empty(t, parsed.Actions, "NoAction handler must not appear in actions:")
}

func TestGenerateFunctionsYAML_emits_handler_and_action_when_ui_provided(t *testing.T) {
	setupYAMLGenTest(t)

	timeout := 15 * time.Second
	registerEchoFn("sw.actions.ban_user",
		Timeout(timeout),
		NameEN("Ban User"),
		NameRU("Заблокировать пользователя"),
		Retry(3, "exponential"),
		Async(),
		Idempotent(),
	)

	var buf bytes.Buffer
	require.NoError(t, GenerateFunctionsYAML(&buf))

	out := buf.String()

	var parsed struct {
		Handlers map[string]map[string]any `yaml:"handlers"`
		Actions  map[string]map[string]any `yaml:"actions"`
	}
	require.NoError(t, yaml.Unmarshal([]byte(out), &parsed))

	// Handler entry — dispatch fields + mirrored display.
	hEntry := parsed.Handlers["sw.actions.ban_user"]
	require.NotNil(t, hEntry, "handler entry expected")
	assert.Equal(t, "15s", hEntry["timeout"])
	assert.Equal(t, true, hEntry["async"])
	assert.Equal(t, true, hEntry["idempotent"])

	retryMap, ok := hEntry["retry"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, 3, retryMap["max_attempts"])
	assert.Equal(t, "exponential", retryMap["backoff"])

	// Display mirrored from actionMeta into handler block.
	hDisplay, ok := hEntry["display"].(map[string]any)
	require.True(t, ok)
	hName, ok := hDisplay["name"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "Ban User", hName["en"])
	assert.Equal(t, "Заблокировать пользователя", hName["ru"])

	// Action entry — separate block, same method key.
	aEntry := parsed.Actions["sw.actions.ban_user"]
	require.NotNil(t, aEntry, "action entry expected because group-C options were supplied")
	assert.Equal(t, "sw.actions.ban_user", aEntry["handler"])
	aDisplay, ok := aEntry["display"].(map[string]any)
	require.True(t, ok)
	aName, ok := aDisplay["name"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "Ban User", aName["en"])
}

func TestGenerateFunctionsYAML_no_action_suppresses_action_block(t *testing.T) {
	setupYAMLGenTest(t)

	// Two UDFs (NoAction) plus one action together. Actions: block must
	// list only the action, not the UDFs.
	registerEchoFn("sw.udfs.alpha", NoAction())
	registerEchoFn("sw.udfs.beta", NoAction())
	registerEchoFn("sw.actions.gamma", NameEN("Gamma"))

	var buf bytes.Buffer
	require.NoError(t, GenerateFunctionsYAML(&buf))

	var parsed struct {
		Handlers map[string]map[string]any `yaml:"handlers"`
		Actions  map[string]map[string]any `yaml:"actions"`
	}
	require.NoError(t, yaml.Unmarshal(buf.Bytes(), &parsed))

	assert.Len(t, parsed.Handlers, 3, "all three should appear in handlers:")
	assert.Len(t, parsed.Actions, 1, "only gamma should appear in actions:")
	_, gammaOk := parsed.Actions["sw.actions.gamma"]
	assert.True(t, gammaOk)
}

func TestGenerateFunctionsYAML_action_forced_without_ui_fields(t *testing.T) {
	setupYAMLGenTest(t)

	// Action() with no group-C option → bare action wrapper, no display
	// block in the action entry, but the entry must exist.
	registerEchoFn("sw.actions.bare", Action())

	var buf bytes.Buffer
	require.NoError(t, GenerateFunctionsYAML(&buf))

	var parsed struct {
		Actions map[string]map[string]any `yaml:"actions"`
	}
	require.NoError(t, yaml.Unmarshal(buf.Bytes(), &parsed))

	aEntry, ok := parsed.Actions["sw.actions.bare"]
	require.True(t, ok, "Action() must force an action wrapper")
	assert.Equal(t, "sw.actions.bare", aEntry["handler"])
	_, hasDisplay := aEntry["display"]
	assert.False(t, hasDisplay, "no group-C options means no display block")
}

func TestGenerateFunctionsYAML_deterministic_alphabetical_order(t *testing.T) {
	setupYAMLGenTest(t)

	// Register in reverse alphabetical order; output should be sorted.
	for _, name := range []string{"z.handler", "m.handler", "a.handler"} {
		registerEchoFn(name, NoAction())
	}

	var buf1, buf2 bytes.Buffer
	require.NoError(t, GenerateFunctionsYAML(&buf1))
	require.NoError(t, GenerateFunctionsYAML(&buf2))

	assert.Equal(t, buf1.String(), buf2.String())

	out := buf1.String()
	posA := strings.Index(out, "a.handler")
	posM := strings.Index(out, "m.handler")
	posZ := strings.Index(out, "z.handler")
	assert.Less(t, posA, posM)
	assert.Less(t, posM, posZ)
}

func TestGenerateFunctionsYAML_name_en_only(t *testing.T) {
	setupYAMLGenTest(t)

	registerEchoFn("sw.actions.hash_partition", NameEN("Hash Partition"))

	var buf bytes.Buffer
	require.NoError(t, GenerateFunctionsYAML(&buf))

	var parsed struct {
		Handlers map[string]map[string]any `yaml:"handlers"`
		Actions  map[string]map[string]any `yaml:"actions"`
	}
	require.NoError(t, yaml.Unmarshal(buf.Bytes(), &parsed))

	hDisplay := parsed.Handlers["sw.actions.hash_partition"]["display"].(map[string]any)
	hName := hDisplay["name"].(map[string]any)
	assert.Equal(t, "Hash Partition", hName["en"])
	assert.Empty(t, hName["ru"])

	aDisplay := parsed.Actions["sw.actions.hash_partition"]["display"].(map[string]any)
	aName := aDisplay["name"].(map[string]any)
	assert.Equal(t, "Hash Partition", aName["en"])
}

func TestRegisterFunction_panics_on_duplicate(t *testing.T) {
	setupYAMLGenTest(t)

	registerEchoFn("dup.method", NoAction())
	assert.Panics(t, func() { registerEchoFn("dup.method", NoAction()) },
		"duplicate registration should panic")
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
// Webhook-flag emission (group B options)
// ---------------------------------------------------------------------------

func TestGenerateFunctionsYAML_unauthenticated(t *testing.T) {
	setupYAMLGenTest(t)

	registerEchoFn("sw.webhook.stripe", Unauthenticated(), NoAction())

	var buf bytes.Buffer
	require.NoError(t, GenerateFunctionsYAML(&buf))

	out := buf.String()
	assert.Contains(t, out, "unauthenticated: true")

	var parsed struct {
		Handlers map[string]map[string]any `yaml:"handlers"`
	}
	require.NoError(t, yaml.Unmarshal([]byte(out), &parsed))
	assert.Equal(t, true, parsed.Handlers["sw.webhook.stripe"]["unauthenticated"])
}

func TestGenerateFunctionsYAML_webhook_full(t *testing.T) {
	setupYAMLGenTest(t)

	registerEchoFn("community.webhook.telegram",
		Unauthenticated(),
		RawBodyAccess(),
		MaxBodyBytes(1<<20),
		RequestDedupKeyExpr("$payload.update_id"),
		TenantFromPayloadLookup(),
		NoAction(),
	)

	var buf bytes.Buffer
	require.NoError(t, GenerateFunctionsYAML(&buf))

	out := buf.String()

	var parsed struct {
		Handlers map[string]map[string]any `yaml:"handlers"`
	}
	require.NoError(t, yaml.Unmarshal([]byte(out), &parsed))

	entry := parsed.Handlers["community.webhook.telegram"]
	require.NotNil(t, entry)
	assert.Equal(t, true, entry["unauthenticated"])
	assert.Equal(t, true, entry["raw_body_access"])
	assert.Equal(t, 1<<20, entry["max_body_bytes"])

	dedup, ok := entry["request_dedup_key"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "expr", dedup["source"])
	assert.Equal(t, "$payload.update_id", dedup["expression"])

	tenantFrom, ok := entry["tenant_from"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "payload_lookup", tenantFrom["source"])
}

func TestGenerateFunctionsYAML_dedup_param(t *testing.T) {
	setupYAMLGenTest(t)

	registerEchoFn("foo.bar", RequestDedupKeyParam("idempotency_key"), NoAction())

	var buf bytes.Buffer
	require.NoError(t, GenerateFunctionsYAML(&buf))

	var parsed struct {
		Handlers map[string]map[string]any `yaml:"handlers"`
	}
	require.NoError(t, yaml.Unmarshal(buf.Bytes(), &parsed))

	dedup, ok := parsed.Handlers["foo.bar"]["request_dedup_key"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "param", dedup["source"])
	assert.Equal(t, "idempotency_key", dedup["param_name"])
}

func TestGenerateFunctionsYAML_tenant_from_header(t *testing.T) {
	setupYAMLGenTest(t)

	registerEchoFn("foo.bar", TenantFromHeader("X-Tenant-Code"), NoAction())

	var buf bytes.Buffer
	require.NoError(t, GenerateFunctionsYAML(&buf))

	var parsed struct {
		Handlers map[string]map[string]any `yaml:"handlers"`
	}
	require.NoError(t, yaml.Unmarshal(buf.Bytes(), &parsed))

	tf, ok := parsed.Handlers["foo.bar"]["tenant_from"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "header", tf["source"])
	assert.Equal(t, "X-Tenant-Code", tf["header_name"])
}

// ---------------------------------------------------------------------------
// Action-block emission for richer options
// ---------------------------------------------------------------------------

func TestGenerateFunctionsYAML_action_destructive_and_longrunning(t *testing.T) {
	setupYAMLGenTest(t)

	registerEchoFn("sw.replay.purge",
		Timeout(30*time.Second),
		NameEN("Purge Replay Artifacts"),
		Icon("trash"),
		Destructive(),
	)
	registerEchoFn("sw.replay.create",
		Timeout(30*time.Second),
		Invoke("unbound"),
		AllowNoObjects(),
		NameEN("Create Replay Job"),
		NameRU("Создать задание реплея"),
		Icon("refresh"),
		LongRunning(),
		ProgressScreen("replay_jobs_list"),
	)

	var buf bytes.Buffer
	require.NoError(t, GenerateFunctionsYAML(&buf))

	var parsed struct {
		Handlers map[string]map[string]any `yaml:"handlers"`
		Actions  map[string]map[string]any `yaml:"actions"`
	}
	require.NoError(t, yaml.Unmarshal(buf.Bytes(), &parsed))

	// Purge — destructive, icon, no long_running, no progress_screen.
	purgeAction := parsed.Actions["sw.replay.purge"]
	require.NotNil(t, purgeAction)
	assert.Equal(t, true, purgeAction["destructive"])
	purgeDisplay := purgeAction["display"].(map[string]any)
	assert.Equal(t, "trash", purgeDisplay["icon"])

	// Create — long_running + progress_screen + EN/RU + icon.
	createAction := parsed.Actions["sw.replay.create"]
	require.NotNil(t, createAction)
	assert.Equal(t, true, createAction["long_running"])
	assert.Equal(t, "replay_jobs_list", createAction["progress_screen"])
	createHandler := parsed.Handlers["sw.replay.create"]
	assert.Equal(t, "unbound", createHandler["invoke"])
	assert.Equal(t, true, createHandler["allow_no_objects"])
}

func TestGenerateFunctionsYAML_required_permission(t *testing.T) {
	setupYAMLGenTest(t)

	registerEchoFn("sw.actions.locked", NameEN("Locked"), RequiredPermission("locked_perm"))

	var buf bytes.Buffer
	require.NoError(t, GenerateFunctionsYAML(&buf))

	var parsed struct {
		Actions map[string]map[string]any `yaml:"actions"`
	}
	require.NoError(t, yaml.Unmarshal(buf.Bytes(), &parsed))

	assert.Equal(t, "locked_perm", parsed.Actions["sw.actions.locked"]["required_permission"])
}
