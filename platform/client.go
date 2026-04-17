package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// MaxResponseSize caps platform API response bodies this client will read
// (response-in, from sidecar's POV). Bulk list endpoints can legitimately
// return tens of MB; 100MB matches the platform's DefaultHandlerResponseLimit
// so a response the platform was willing to send always fits here.
//
// Enforced via http.MaxBytesReader so oversized responses surface as a
// specific APIError rather than being silently truncated into a misleading
// JSON parse error downstream.
const MaxResponseSize int64 = 100 * 1024 * 1024

// Config configures the platform client.
type Config struct {
	// BaseURL is the Declarion platform base URL (e.g. "http://declarion:3000").
	BaseURL string

	// Token is the continuation token forwarded on every callback.
	Token string

	// Traceparent is the W3C traceparent header propagated on every callback.
	Traceparent string

	// Baggage is the W3C baggage header propagated on every callback.
	Baggage string

	// HTTPClient overrides the default HTTP client. Useful for testing.
	HTTPClient *http.Client
}

// Client provides typed access to Declarion platform APIs.
// Auto-attaches the continuation token and trace headers on every request.
type Client struct {
	baseURL     string
	token       string
	traceparent string
	baggage     string
	http        *http.Client
}

// New creates a platform client with the given config.
func New(cfg Config) *Client {
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 60 * time.Second}
	}
	return &Client{
		baseURL:     strings.TrimRight(cfg.BaseURL, "/"),
		token:       cfg.Token,
		traceparent: cfg.Traceparent,
		baggage:     cfg.Baggage,
		http:        httpClient,
	}
}

// Token returns the continuation token this client uses.
func (c *Client) Token() string { return c.token }

// Traceparent returns the W3C traceparent header this client propagates.
func (c *Client) Traceparent() string { return c.traceparent }

// Baggage returns the W3C baggage header this client propagates.
func (c *Client) Baggage() string { return c.baggage }

// Data returns the data API sub-client.
func (c *Client) Data() *DataClient {
	return &DataClient{c: c}
}

// Actions returns the actions API sub-client.
func (c *Client) Actions() *ActionsClient {
	return &ActionsClient{c: c}
}

// Params returns the params API sub-client.
func (c *Client) Params() *ParamsClient {
	return &ParamsClient{c: c}
}

// do executes an HTTP request with all required headers.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body any) ([]byte, int, error) {
	if c.baseURL == "" {
		return nil, 0, fmt.Errorf("platform client: BaseURL not configured (set DECLARION_PLATFORM_URL)")
	}

	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, 0, fmt.Errorf("marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(b)
	}

	u := c.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, method, u, bodyReader)
	if err != nil {
		return nil, 0, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if c.traceparent != "" {
		req.Header.Set("traceparent", c.traceparent)
	}
	if c.baggage != "" {
		req.Header.Set("baggage", c.baggage)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("request %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	// MaxBytesReader surfaces *http.MaxBytesError on overflow so callers see
	// "response exceeded N bytes" instead of a silently-truncated body that
	// downstream JSON parse would misreport as "unexpected end of JSON input".
	respBody, err := io.ReadAll(http.MaxBytesReader(nil, resp.Body, MaxResponseSize))
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return nil, resp.StatusCode, fmt.Errorf("platform %s %s: response exceeded %d bytes (limit %d)", method, path, maxErr.Limit, MaxResponseSize)
		}
		return nil, resp.StatusCode, fmt.Errorf("read response: %w", err)
	}

	return respBody, resp.StatusCode, nil
}

// APIError represents a non-2xx response from the platform.
type APIError struct {
	StatusCode int
	Body       string
	Path       string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("platform API %s: HTTP %d: %s", e.Path, e.StatusCode, e.Body)
}
