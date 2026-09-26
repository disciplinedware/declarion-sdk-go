package runtime

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/disciplinedware/declarion-sdk-go/platform"
)

const testPlatformIdleConnTimeout = 30 * time.Second

func TestConnectPlatform(t *testing.T) {
	cases := []struct {
		name     string
		field    time.Duration
		env      string
		wantIdle time.Duration
		wantErr  string
	}{
		{name: "field_is_used", field: 20 * time.Second, wantIdle: 20 * time.Second},
		{name: "env_is_read_when_field_is_zero", env: "45s", wantIdle: 45 * time.Second},
		{name: "field_outranks_env", field: 20 * time.Second, env: "45s", wantIdle: 20 * time.Second},
		{name: "unset_takes_the_platform_default", wantIdle: platform.DefaultIdleConnTimeout},
		{name: "unparsable_env_is_refused", env: "thirty", wantErr: `DECLARION_PLATFORM_IDLE_CONN_TIMEOUT="thirty" is not a duration`},
		{name: "zero_env_is_refused", env: "0s", wantErr: "must be positive"},
		{name: "negative_field_is_refused", field: -time.Second, wantErr: "must be positive"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(envPlatformIdleConnTimeout, tc.env)
			cfg := &Config{PlatformIdleConnTimeout: tc.field}
			err := cfg.connectPlatform()
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				assert.Nil(t, cfg.platformHTTP)
				return
			}
			require.NoError(t, err)
			transport, ok := cfg.platformHTTP.Transport.(*http.Transport)
			require.True(t, ok)
			assert.Equal(t, tc.wantIdle, transport.IdleConnTimeout)
			assert.Zero(t, cfg.platformHTTP.Timeout, "a client-level timeout would override the request's context")
		})
	}
}

func TestNewHandlerRefusesAnUnparsableIdleConnTimeout(t *testing.T) {
	t.Setenv(envPlatformIdleConnTimeout, "thirty")
	_, err := NewHandler(Config{Authenticator: &testRequestAuthenticator{}, Logger: zap.NewNop()})
	require.ErrorContains(t, err, "is not a duration")
}
