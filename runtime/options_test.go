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
