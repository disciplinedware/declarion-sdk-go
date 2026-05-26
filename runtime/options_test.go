package runtime

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// HandlerOption getter tests
// ---------------------------------------------------------------------------

func TestTimeout_sets_metadata(t *testing.T) {
	reg := Handler("test.timeout", func(_ *Ctx, p echoParams) (echoResult, error) {
		return echoResult{}, nil
	}, Timeout(15*time.Second))

	require.NotNil(t, reg.Metadata.Timeout)
	assert.Equal(t, 15*time.Second, *reg.Metadata.Timeout)
}

func TestNameEN_sets_metadata(t *testing.T) {
	reg := Handler("test.nameen", func(_ *Ctx, p echoParams) (echoResult, error) {
		return echoResult{}, nil
	}, NameEN("My Handler"))

	assert.Equal(t, "My Handler", reg.Metadata.NameEN)
}

func TestNameRU_sets_metadata(t *testing.T) {
	reg := Handler("test.nameru", func(_ *Ctx, p echoParams) (echoResult, error) {
		return echoResult{}, nil
	}, NameRU("Мой обработчик"))

	assert.Equal(t, "Мой обработчик", reg.Metadata.NameRU)
}

func TestAsync_sets_metadata(t *testing.T) {
	reg := Handler("test.async", func(_ *Ctx, p echoParams) (echoResult, error) {
		return echoResult{}, nil
	}, Async())

	assert.True(t, reg.Metadata.IsAsync)
}

func TestRetry_sets_metadata(t *testing.T) {
	reg := Handler("test.retry", func(_ *Ctx, p echoParams) (echoResult, error) {
		return echoResult{}, nil
	}, Retry(3, "exponential"))

	require.NotNil(t, reg.Metadata.Retry)
	assert.Equal(t, 3, reg.Metadata.Retry.MaxAttempts)
	assert.Equal(t, "exponential", reg.Metadata.Retry.Backoff)
}

func TestRetry_panics_on_invalid_max_attempts(t *testing.T) {
	assert.Panics(t, func() {
		Retry(1, "exponential") // must be >= 2
	})
}

func TestRetry_panics_on_invalid_backoff(t *testing.T) {
	assert.Panics(t, func() {
		Retry(2, "random") // not in allowed set
	})
}

func TestRetry_all_valid_backoffs(t *testing.T) {
	for _, backoff := range []string{"exponential", "linear", "constant"} {
		backoff := backoff
		t.Run(backoff, func(t *testing.T) {
			assert.NotPanics(t, func() { Retry(2, backoff) })
		})
	}
}

func TestHandler_multiple_options_combined(t *testing.T) {
	timeout := 30 * time.Second
	reg := Handler("test.combined", func(_ *Ctx, p echoParams) (echoResult, error) {
		return echoResult{}, nil
	},
		Timeout(timeout),
		NameEN("Combined Handler"),
		NameRU("Комбинированный обработчик"),
		Retry(5, "linear"),
		Async(),
	)

	require.NotNil(t, reg.Metadata.Timeout)
	assert.Equal(t, timeout, *reg.Metadata.Timeout)
	assert.Equal(t, "Combined Handler", reg.Metadata.NameEN)
	assert.Equal(t, "Комбинированный обработчик", reg.Metadata.NameRU)
	require.NotNil(t, reg.Metadata.Retry)
	assert.Equal(t, 5, reg.Metadata.Retry.MaxAttempts)
	assert.Equal(t, "linear", reg.Metadata.Retry.Backoff)
	assert.True(t, reg.Metadata.IsAsync)
}

func TestHandler_no_options_leaves_zero_metadata(t *testing.T) {
	reg := Handler("test.bare", func(_ *Ctx, p echoParams) (echoResult, error) {
		return echoResult{}, nil
	})

	assert.Nil(t, reg.Metadata.Timeout)
	assert.Empty(t, reg.Metadata.NameEN)
	assert.Empty(t, reg.Metadata.NameRU)
	assert.Nil(t, reg.Metadata.Retry)
	assert.False(t, reg.Metadata.IsAsync)
}

// ---------------------------------------------------------------------------
// Webhook-flag option setters
// ---------------------------------------------------------------------------

func TestUnauthenticated_sets_metadata(t *testing.T) {
	reg := Handler("test.uwh", func(_ *Ctx, p echoParams) (echoResult, error) {
		return echoResult{}, nil
	}, Unauthenticated())
	assert.True(t, reg.Metadata.IsUnauthenticated)
}

func TestRawBodyAccess_sets_metadata(t *testing.T) {
	reg := Handler("test.raw", func(_ *Ctx, p echoParams) (echoResult, error) {
		return echoResult{}, nil
	}, RawBodyAccess())
	assert.True(t, reg.Metadata.HasRawBodyAccess)
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
			reg := Handler("test.cap", func(_ *Ctx, p echoParams) (echoResult, error) {
				return echoResult{}, nil
			}, MaxBodyBytes(tc.bytes))
			assert.Equal(t, tc.bytes, reg.Metadata.MaxBodyBytes)
		})
	}
}

func TestMaxBodyBytes_panics_on_negative(t *testing.T) {
	assert.Panics(t, func() { MaxBodyBytes(-1) })
}

func TestRequestDedupKeyExpr_sets_metadata(t *testing.T) {
	reg := Handler("test.dedup", func(_ *Ctx, p echoParams) (echoResult, error) {
		return echoResult{}, nil
	}, RequestDedupKeyExpr("$payload.update_id"))
	require.NotNil(t, reg.Metadata.RequestDedupKey)
	assert.Equal(t, "expr", reg.Metadata.RequestDedupKey.Source)
	assert.Equal(t, "$payload.update_id", reg.Metadata.RequestDedupKey.Expression)
}

func TestRequestDedupKeyExpr_panics_on_empty(t *testing.T) {
	assert.Panics(t, func() { RequestDedupKeyExpr("") })
}

func TestRequestDedupKeyParam_sets_metadata(t *testing.T) {
	reg := Handler("test.dedup", func(_ *Ctx, p echoParams) (echoResult, error) {
		return echoResult{}, nil
	}, RequestDedupKeyParam("idempotency_key"))
	require.NotNil(t, reg.Metadata.RequestDedupKey)
	assert.Equal(t, "param", reg.Metadata.RequestDedupKey.Source)
	assert.Equal(t, "idempotency_key", reg.Metadata.RequestDedupKey.ParamName)
}

func TestRequestDedupKeyParam_panics_on_empty(t *testing.T) {
	assert.Panics(t, func() { RequestDedupKeyParam("") })
}

func TestTenantFromHeader_sets_metadata(t *testing.T) {
	reg := Handler("test.tf", func(_ *Ctx, p echoParams) (echoResult, error) {
		return echoResult{}, nil
	}, TenantFromHeader("X-Tenant-Code"))
	require.NotNil(t, reg.Metadata.TenantFrom)
	assert.Equal(t, "header", reg.Metadata.TenantFrom.Source)
	assert.Equal(t, "X-Tenant-Code", reg.Metadata.TenantFrom.HeaderName)
}

func TestTenantFromHeader_panics_on_empty(t *testing.T) {
	assert.Panics(t, func() { TenantFromHeader("") })
}

func TestTenantFromPayloadLookup_sets_metadata(t *testing.T) {
	reg := Handler("test.tf", func(_ *Ctx, p echoParams) (echoResult, error) {
		return echoResult{}, nil
	}, TenantFromPayloadLookup())
	require.NotNil(t, reg.Metadata.TenantFrom)
	assert.Equal(t, "payload_lookup", reg.Metadata.TenantFrom.Source)
}
