package platform

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewTransport(t *testing.T) {
	t.Run("carries_the_declared_idle_timeout", func(t *testing.T) {
		transport, err := NewTransport(25 * time.Second)
		require.NoError(t, err)
		assert.Equal(t, 25*time.Second, transport.IdleConnTimeout)
		assert.NotSame(t, http.DefaultTransport, transport, "a caller's pool must not be the process-wide default")
	})
	for _, bad := range []time.Duration{0, -time.Second} {
		t.Run("refuses_"+bad.String(), func(t *testing.T) {
			_, err := NewTransport(bad)
			require.ErrorContains(t, err, "must be positive")
		})
	}
}

func TestNewRefusesAClientWithoutTransport(t *testing.T) {
	cases := map[string]*http.Client{
		"no_client":    nil,
		"no_transport": {},
	}
	for name, client := range cases {
		t.Run(name, func(t *testing.T) {
			assert.PanicsWithValue(t,
				"platform.New: Config.HTTPClient with a Transport from platform.NewTransport is required",
				func() { New(Config{BaseURL: "http://unused.invalid", HTTPClient: client}) })
		})
	}
}

// The client a caller passes is the one that carries the request.
func TestNewSendsThroughTheGivenClient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()
	transport, err := NewTransport(25 * time.Second)
	require.NoError(t, err)
	counting := &countingTransport{base: transport}

	_, err = New(Config{BaseURL: srv.URL, HTTPClient: &http.Client{Transport: counting}}).Data().List(t.Context(), "lead", ListParams{})
	require.NoError(t, err)
	assert.Equal(t, 1, counting.requests)
}

type countingTransport struct {
	base     http.RoundTripper
	requests int
}

func (c *countingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	c.requests++
	return c.base.RoundTrip(req)
}
