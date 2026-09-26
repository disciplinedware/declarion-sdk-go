package platform

import (
	"fmt"
	"net/http"
	"time"
)

// DefaultIdleConnTimeout is half the platform's default `http_idle_timeout`
// (60 s): what a client keeps an idle connection when its caller declares none.
const DefaultIdleConnTimeout = 30 * time.Second

// NewTransport returns the connection pool a client keeps to the Declarion
// platform, closing a pooled connection once it has been idle idleConnTimeout.
//
// idleConnTimeout MUST be shorter than the platform's `http_idle_timeout`. The
// platform closes a connection idle that long; a client that keeps it longer
// sends its next request down a socket the server already closed, and the
// request fails with `connection reset by peer` or `EOF`. Go retries such a
// request only when it is idempotent, so a POST - most platform calls - fails.
//
// The value is the caller's declared setting. Zero or negative is refused,
// never replaced by a default.
func NewTransport(idleConnTimeout time.Duration) (*http.Transport, error) {
	if idleConnTimeout <= 0 {
		return nil, fmt.Errorf("platform transport: the idle connection timeout must be positive, got %s", idleConnTimeout)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.IdleConnTimeout = idleConnTimeout
	return transport, nil
}
