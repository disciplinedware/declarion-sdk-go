package runtime

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// findRegistration looks up the package-level registration record by method
// name. Tests use this after RegisterFunction to inspect what the options
// wrote into handlerMeta / actionMeta.
func findRegistration(t *testing.T, method string) registration {
	t.Helper()
	for _, r := range handlerRegistry {
		if r.method == method {
			return r
		}
	}
	t.Fatalf("registration %q not found", method)
	return registration{}
}

func registerEcho(method string, opts ...Option) {
	RegisterFunction[echoParams, echoResult](method, func(_ *Ctx, _ echoParams) (echoResult, error) {
		return echoResult{}, nil
	}, opts...)
}

// ---------------------------------------------------------------------------
// Group A — handler dispatch
// ---------------------------------------------------------------------------

func TestTimeout_sets_metadata(t *testing.T) {
	ClearHandlerRegistry()
	t.Cleanup(ClearHandlerRegistry)
	registerEcho("test.timeout", Timeout(15*time.Second))
	r := findRegistration(t, "test.timeout")
	require.NotNil(t, r.handlerMeta.Timeout)
	assert.Equal(t, 15*time.Second, *r.handlerMeta.Timeout)
}

func TestAsync_sets_metadata(t *testing.T) {
	ClearHandlerRegistry()
	t.Cleanup(ClearHandlerRegistry)
	registerEcho("test.async", Async())
	r := findRegistration(t, "test.async")
	assert.True(t, r.handlerMeta.IsAsync)
}

func TestRetry_sets_metadata(t *testing.T) {
	ClearHandlerRegistry()
	t.Cleanup(ClearHandlerRegistry)
	registerEcho("test.retry", Retry(3, "exponential"))
	r := findRegistration(t, "test.retry")
	require.NotNil(t, r.handlerMeta.Retry)
	assert.Equal(t, 3, r.handlerMeta.Retry.MaxAttempts)
	assert.Equal(t, "exponential", r.handlerMeta.Retry.Backoff)
}

func TestRetry_panics_on_invalid_max_attempts(t *testing.T) {
	assert.Panics(t, func() { Retry(1, "exponential") })
}

func TestRetry_panics_on_invalid_backoff(t *testing.T) {
	assert.Panics(t, func() { Retry(2, "random") })
}

func TestRetry_all_valid_backoffs(t *testing.T) {
	for _, backoff := range []string{"exponential", "linear", "constant"} {
		backoff := backoff
		t.Run(backoff, func(t *testing.T) {
			assert.NotPanics(t, func() { Retry(2, backoff) })
		})
	}
}

func TestIdempotent_sets_metadata(t *testing.T) {
	ClearHandlerRegistry()
	t.Cleanup(ClearHandlerRegistry)
	registerEcho("test.idem", Idempotent())
	assert.True(t, findRegistration(t, "test.idem").handlerMeta.Idempotent)
}

func TestInvoke_sets_metadata(t *testing.T) {
	ClearHandlerRegistry()
	t.Cleanup(ClearHandlerRegistry)
	registerEcho("test.invoke", Invoke("unbound"))
	assert.Equal(t, "unbound", findRegistration(t, "test.invoke").handlerMeta.Invoke)
}

func TestInvoke_panics_on_unknown(t *testing.T) {
	assert.Panics(t, func() { Invoke("garbage") })
}

func TestAllowNoObjects_sets_metadata(t *testing.T) {
	ClearHandlerRegistry()
	t.Cleanup(ClearHandlerRegistry)
	registerEcho("test.ano", AllowNoObjects())
	assert.True(t, findRegistration(t, "test.ano").handlerMeta.AllowNoObjects)
}

func TestGlobalOnly_sets_metadata(t *testing.T) {
	ClearHandlerRegistry()
	t.Cleanup(ClearHandlerRegistry)
	registerEcho("test.global_only", GlobalOnly())
	assert.True(t, findRegistration(t, "test.global_only").handlerMeta.GlobalOnly)
}

func TestReadOnly_sets_metadata(t *testing.T) {
	ClearHandlerRegistry()
	t.Cleanup(ClearHandlerRegistry)
	registerEcho("test.ro", ReadOnly())
	assert.True(t, findRegistration(t, "test.ro").handlerMeta.ReadOnly)
}

func TestSuppressEvents_sets_metadata(t *testing.T) {
	ClearHandlerRegistry()
	t.Cleanup(ClearHandlerRegistry)
	registerEcho("test.se", SuppressEvents())
	assert.True(t, findRegistration(t, "test.se").handlerMeta.SuppressEvents)
}

func TestAudit_sets_metadata(t *testing.T) {
	ClearHandlerRegistry()
	t.Cleanup(ClearHandlerRegistry)
	registerEcho("test.audit_true", Audit(true))
	registerEcho("test.audit_false", Audit(false))
	a := findRegistration(t, "test.audit_true").handlerMeta.Audit
	b := findRegistration(t, "test.audit_false").handlerMeta.Audit
	require.NotNil(t, a)
	require.NotNil(t, b)
	assert.True(t, *a)
	assert.False(t, *b)
}

// ---------------------------------------------------------------------------
// Group B — webhook/auth
// ---------------------------------------------------------------------------

func TestUnauthenticated_sets_metadata(t *testing.T) {
	ClearHandlerRegistry()
	t.Cleanup(ClearHandlerRegistry)
	registerEcho("test.uwh", Unauthenticated())
	assert.True(t, findRegistration(t, "test.uwh").handlerMeta.IsUnauthenticated)
}

func TestRawBodyAccess_sets_metadata(t *testing.T) {
	ClearHandlerRegistry()
	t.Cleanup(ClearHandlerRegistry)
	registerEcho("test.raw", RawBodyAccess())
	assert.True(t, findRegistration(t, "test.raw").handlerMeta.HasRawBodyAccess)
}

func TestMaxBodyBytes_table(t *testing.T) {
	cases := []struct {
		name  string
		bytes int64
	}{
		{"one_kb", 1 << 10},
		{"one_mb", 1 << 20},
		{"sixteen_mb", 16 << 20},
		{"zero_means_default", 0},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			ClearHandlerRegistry()
			t.Cleanup(ClearHandlerRegistry)
			registerEcho("test.cap", MaxBodyBytes(tc.bytes))
			assert.Equal(t, tc.bytes, findRegistration(t, "test.cap").handlerMeta.MaxBodyBytes)
		})
	}
}

func TestMaxBodyBytes_panics_on_negative(t *testing.T) {
	assert.Panics(t, func() { MaxBodyBytes(-1) })
}

func TestRequestVerifier_sets_metadata(t *testing.T) {
	ClearHandlerRegistry()
	t.Cleanup(ClearHandlerRegistry)
	registerEcho("test.rv", RequestVerifier("telegram"))
	assert.Equal(t, "telegram", findRegistration(t, "test.rv").handlerMeta.RequestVerifier)
}

func TestRequestVerifier_panics_on_empty(t *testing.T) {
	assert.Panics(t, func() { RequestVerifier("") })
}

func TestRequestDedupKeyExpr_sets_metadata(t *testing.T) {
	ClearHandlerRegistry()
	t.Cleanup(ClearHandlerRegistry)
	registerEcho("test.dedup", RequestDedupKeyExpr("$payload.update_id"))
	dk := findRegistration(t, "test.dedup").handlerMeta.RequestDedupKey
	require.NotNil(t, dk)
	assert.Equal(t, "expr", dk.Source)
	assert.Equal(t, "$payload.update_id", dk.Expression)
}

func TestRequestDedupKeyExpr_panics_on_empty(t *testing.T) {
	assert.Panics(t, func() { RequestDedupKeyExpr("") })
}

func TestRequestDedupKeyParam_sets_metadata(t *testing.T) {
	ClearHandlerRegistry()
	t.Cleanup(ClearHandlerRegistry)
	registerEcho("test.dedup", RequestDedupKeyParam("idempotency_key"))
	dk := findRegistration(t, "test.dedup").handlerMeta.RequestDedupKey
	require.NotNil(t, dk)
	assert.Equal(t, "param", dk.Source)
	assert.Equal(t, "idempotency_key", dk.ParamName)
}

func TestRequestDedupKeyParam_panics_on_empty(t *testing.T) {
	assert.Panics(t, func() { RequestDedupKeyParam("") })
}

func TestTenantFromHeader_sets_metadata(t *testing.T) {
	ClearHandlerRegistry()
	t.Cleanup(ClearHandlerRegistry)
	registerEcho("test.tf", TenantFromHeader("X-Tenant-Code"))
	tf := findRegistration(t, "test.tf").handlerMeta.TenantFrom
	require.NotNil(t, tf)
	assert.Equal(t, "header", tf.Source)
	assert.Equal(t, "X-Tenant-Code", tf.HeaderName)
}

func TestTenantFromHeader_panics_on_empty(t *testing.T) {
	assert.Panics(t, func() { TenantFromHeader("") })
}

func TestTenantFromPayloadLookup_sets_metadata(t *testing.T) {
	ClearHandlerRegistry()
	t.Cleanup(ClearHandlerRegistry)
	registerEcho("test.tf", TenantFromPayloadLookup())
	tf := findRegistration(t, "test.tf").handlerMeta.TenantFrom
	require.NotNil(t, tf)
	assert.Equal(t, "payload_lookup", tf.Source)
}

// ---------------------------------------------------------------------------
// Group C — action UI/metadata (implies action wrapper)
// ---------------------------------------------------------------------------

func TestNameEN_initializes_actionMeta(t *testing.T) {
	ClearHandlerRegistry()
	t.Cleanup(ClearHandlerRegistry)
	registerEcho("test.nameen", NameEN("My Action"))
	r := findRegistration(t, "test.nameen")
	require.NotNil(t, r.actionMeta)
	assert.Equal(t, "My Action", r.actionMeta.Display.NameEN)
}

func TestNameRU_initializes_actionMeta(t *testing.T) {
	ClearHandlerRegistry()
	t.Cleanup(ClearHandlerRegistry)
	registerEcho("test.nameru", NameRU("Моё действие"))
	r := findRegistration(t, "test.nameru")
	require.NotNil(t, r.actionMeta)
	assert.Equal(t, "Моё действие", r.actionMeta.Display.NameRU)
}

func TestIcon_initializes_actionMeta(t *testing.T) {
	ClearHandlerRegistry()
	t.Cleanup(ClearHandlerRegistry)
	registerEcho("test.icon", Icon("trash"))
	r := findRegistration(t, "test.icon")
	require.NotNil(t, r.actionMeta)
	assert.Equal(t, "trash", r.actionMeta.Display.Icon)
}

func TestDestructive_initializes_actionMeta(t *testing.T) {
	ClearHandlerRegistry()
	t.Cleanup(ClearHandlerRegistry)
	registerEcho("test.dest", Destructive())
	r := findRegistration(t, "test.dest")
	require.NotNil(t, r.actionMeta)
	assert.True(t, r.actionMeta.Destructive)
}

func TestLongRunning_initializes_actionMeta(t *testing.T) {
	ClearHandlerRegistry()
	t.Cleanup(ClearHandlerRegistry)
	registerEcho("test.lr", LongRunning())
	r := findRegistration(t, "test.lr")
	require.NotNil(t, r.actionMeta)
	assert.True(t, r.actionMeta.LongRunning)
}

func TestProgressScreen_initializes_actionMeta(t *testing.T) {
	ClearHandlerRegistry()
	t.Cleanup(ClearHandlerRegistry)
	registerEcho("test.ps", ProgressScreen("replay_jobs_list"))
	r := findRegistration(t, "test.ps")
	require.NotNil(t, r.actionMeta)
	assert.Equal(t, "replay_jobs_list", r.actionMeta.ProgressScreen)
}

func TestProgressScreen_panics_on_empty(t *testing.T) {
	assert.Panics(t, func() { ProgressScreen("") })
}

// ---------------------------------------------------------------------------
// Group D — action gate control
// ---------------------------------------------------------------------------

func TestAction_forces_action_wrapper(t *testing.T) {
	ClearHandlerRegistry()
	t.Cleanup(ClearHandlerRegistry)
	registerEcho("test.bare_action", Action())
	r := findRegistration(t, "test.bare_action")
	require.NotNil(t, r.actionMeta, "Action() must initialize a bare actionMeta")
}

func TestNoAction_suppresses_actionMeta(t *testing.T) {
	ClearHandlerRegistry()
	t.Cleanup(ClearHandlerRegistry)
	registerEcho("test.udf", NoAction())
	r := findRegistration(t, "test.udf")
	assert.Nil(t, r.actionMeta, "NoAction() must leave actionMeta nil")
}

func TestNoAction_panics_when_combined_with_groupC(t *testing.T) {
	ClearHandlerRegistry()
	t.Cleanup(ClearHandlerRegistry)
	assert.Panics(t, func() {
		registerEcho("test.conflict", NoAction(), NameEN("oops"))
	})
}

func TestNoAction_panics_when_combined_with_Action(t *testing.T) {
	ClearHandlerRegistry()
	t.Cleanup(ClearHandlerRegistry)
	assert.Panics(t, func() {
		registerEcho("test.conflict", NoAction(), Action())
	})
}

func TestInternal_initializes_actionMeta(t *testing.T) {
	ClearHandlerRegistry()
	t.Cleanup(ClearHandlerRegistry)
	registerEcho("test.internal", Internal())
	r := findRegistration(t, "test.internal")
	require.NotNil(t, r.actionMeta, "Internal() must force an action wrapper")
	assert.True(t, r.actionMeta.Internal)
}

func TestNoAction_panics_when_combined_with_Internal(t *testing.T) {
	ClearHandlerRegistry()
	t.Cleanup(ClearHandlerRegistry)
	assert.Panics(t, func() {
		registerEcho("test.conflict", NoAction(), Internal())
	})
}

func TestRequiredPermission_initializes_actionMeta(t *testing.T) {
	ClearHandlerRegistry()
	t.Cleanup(ClearHandlerRegistry)
	registerEcho("test.perm", RequiredPermission("custom_perm"))
	r := findRegistration(t, "test.perm")
	require.NotNil(t, r.actionMeta)
	assert.Equal(t, "custom_perm", r.actionMeta.RequiredPermission)
}

func TestRequiredPermission_panics_on_empty(t *testing.T) {
	assert.Panics(t, func() { RequiredPermission("") })
}

// ---------------------------------------------------------------------------
// Combined / shape sanity
// ---------------------------------------------------------------------------

func TestRegisterFunction_no_options_leaves_zero_metadata(t *testing.T) {
	ClearHandlerRegistry()
	t.Cleanup(ClearHandlerRegistry)
	registerEcho("test.bare")
	r := findRegistration(t, "test.bare")
	assert.Nil(t, r.handlerMeta.Timeout)
	assert.Nil(t, r.handlerMeta.Retry)
	assert.False(t, r.handlerMeta.IsAsync)
	assert.Nil(t, r.actionMeta, "bare registration should produce no action wrapper")
}

func TestRegisterFunction_multiple_options_combined(t *testing.T) {
	ClearHandlerRegistry()
	t.Cleanup(ClearHandlerRegistry)
	timeout := 30 * time.Second
	registerEcho("test.combined",
		Timeout(timeout),
		NameEN("Combined Handler"),
		NameRU("Комбинированный обработчик"),
		Retry(5, "linear"),
		Async(),
		Icon("bolt"),
		LongRunning(),
	)
	r := findRegistration(t, "test.combined")
	require.NotNil(t, r.handlerMeta.Timeout)
	assert.Equal(t, timeout, *r.handlerMeta.Timeout)
	require.NotNil(t, r.actionMeta)
	assert.Equal(t, "Combined Handler", r.actionMeta.Display.NameEN)
	assert.Equal(t, "Комбинированный обработчик", r.actionMeta.Display.NameRU)
	assert.Equal(t, "bolt", r.actionMeta.Display.Icon)
	assert.True(t, r.actionMeta.LongRunning)
	require.NotNil(t, r.handlerMeta.Retry)
	assert.Equal(t, 5, r.handlerMeta.Retry.MaxAttempts)
	assert.Equal(t, "linear", r.handlerMeta.Retry.Backoff)
	assert.True(t, r.handlerMeta.IsAsync)
}
