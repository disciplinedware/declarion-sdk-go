package platform

import (
	"errors"
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

func TestNewBuildsThePlatformTransportWhenGivenNone(t *testing.T) {
	cases := map[string]*http.Client{
		"no_client":    nil,
		"no_transport": {Timeout: 7 * time.Second},
	}
	for name, client := range cases {
		t.Run(name, func(t *testing.T) {
			c := New(Config{BaseURL: "http://unused.invalid", HTTPClient: client})
			transport, ok := c.http.Transport.(*http.Transport)
			require.True(t, ok)
			assert.Equal(t, DefaultIdleConnTimeout, transport.IdleConnTimeout)
			if client != nil {
				assert.Equal(t, client.Timeout, c.http.Timeout)
			}
		})
	}
}

// A client made per call must not open a pool per call.
func TestClientsBuiltWithoutAClientShareOneTransport(t *testing.T) {
	first := New(Config{BaseURL: "http://unused.invalid"})
	second := New(Config{BaseURL: "http://unused.invalid"})
	assert.Same(t, first.http.Transport, second.http.Transport)
}

// A default transport that cannot be built fails the request, not the process.
func TestAnUnbuildableDefaultTransportFailsTheRequest(t *testing.T) {
	_, err := failingTransport{err: errors.New("no pool")}.RoundTrip(nil)
	assert.EqualError(t, err, "no pool")
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

func TestNewTransportRefusesAReplacedDefaultTransport(t *testing.T) {
	saved := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = saved })
	http.DefaultTransport = roundTripperFunc(func(*http.Request) (*http.Response, error) { return nil, nil })
	_, err := NewTransport(DefaultIdleConnTimeout)
	require.ErrorContains(t, err, "not an *http.Transport")
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
