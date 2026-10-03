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

	"github.com/disciplinedware/declarion-sdk-go/execution"
	"github.com/disciplinedware/declarion-sdk-go/tracing"
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

// Target-tenant headers let API clients execute a request in a tenant that is
// different from the credential's home tenant. Set at most one per request.
const (
	TargetTenantIDHeader   = "X-Declarion-Tenant-ID"
	TargetTenantCodeHeader = "X-Declarion-Tenant-Code"
)

// Config configures the platform client.
type Config struct {
	// BaseURL is the Declarion platform base URL (e.g. "http://declarion:3000").
	BaseURL string

	// Token is the continuation token forwarded on every callback.
	Token string

	// TargetTenantID sends X-Declarion-Tenant-ID on every request. Mutually
	// exclusive with TargetTenantCode.
	TargetTenantID string

	// TargetTenantCode sends X-Declarion-Tenant-Code on every request. Mutually
	// exclusive with TargetTenantID.
	TargetTenantCode string

	// HTTPClient carries every request. Required, with a Transport built by
	// NewTransport. Share one across clients: each per-token client then reuses
	// the same connection pool. Leave its Timeout zero - a request is bounded by
	// its context, and a client-level Timeout silently overrides that context: a
	// 60 s cap once cut an 85 s connector inference the server kept running and
	// charging, and the caller's retry charged it twice.
	HTTPClient *http.Client
}

// Client provides typed access to Declarion platform APIs.
// Auto-attaches the continuation token and trace headers on every request.
type Client struct {
	elevation    execution.Elevation
	selectionErr error
	selected     bool
	baseURL      string
	token        string
	tenantID     string
	tenantCode   string
	http         *http.Client
}

// New creates a platform client with the given config. With no cfg.HTTPClient,
// or one without a Transport, it uses the process's one default transport
// (defaultTransport) - never Go's default transport, which keeps an idle
// connection 90 s, longer than the platform does.
func New(cfg Config) *Client {
	elevation, selectionErr := execution.InheritedSelection(cfg.Token)
	httpClient := cfg.HTTPClient
	if httpClient == nil || httpClient.Transport == nil {
		pooled := &http.Client{Transport: defaultTransport()}
		if httpClient != nil {
			*pooled = *httpClient
			pooled.Transport = defaultTransport()
		}
		httpClient = pooled
	}
	httpClient = tracing.HTTPClient(httpClient, cfg.BaseURL)
	return &Client{
		elevation:    elevation,
		selectionErr: selectionErr,
		baseURL:      strings.TrimRight(cfg.BaseURL, "/"),
		token:        cfg.Token,
		tenantID:     cfg.TargetTenantID,
		tenantCode:   cfg.TargetTenantCode,
		http:         httpClient,
	}
}

// RequestOption customizes one platform request.
type RequestOption func(*requestOptions)

type requestOptions struct {
	tenantID   string
	tenantCode string
}

// WithTargetTenantID sends X-Declarion-Tenant-ID for this request.
func WithTargetTenantID(tenantID string) RequestOption {
	return func(o *requestOptions) {
		o.tenantID = tenantID
		o.tenantCode = ""
	}
}

// WithTargetTenantCode sends X-Declarion-Tenant-Code for this request.
func WithTargetTenantCode(tenantCode string) RequestOption {
	return func(o *requestOptions) {
		o.tenantCode = tenantCode
		o.tenantID = ""
	}
}

func targetTenantOptions(tenantID, tenantCode string) []RequestOption {
	if tenantID != "" || tenantCode != "" {
		return []RequestOption{func(o *requestOptions) {
			o.tenantID = tenantID
			o.tenantCode = tenantCode
		}}
	}
	return nil
}

// Token returns the continuation token this client uses.
func (c *Client) Token() string { return c.token }

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

// MCP returns the client for Declarion's Model Context Protocol endpoint.
// It uses the same bearer, trace, and tenant scope as the other platform
// clients.
func (c *Client) MCP() *MCPClient {
	return &MCPClient{c: c}
}

// newRequest builds an *http.Request with the platform's auth, trace, and
// target-tenant headers applied. Shared by the buffered `do` path and the
// streaming Actions().InvokeStreaming path so header construction and the
// dk:/Bearer auth rule live in one place.
func (c *Client) newRequest(ctx context.Context, method, path string, query url.Values, body any, opts ...RequestOption) (*http.Request, error) {
	if c.baseURL == "" {
		return nil, fmt.Errorf("platform client: BaseURL not configured (set DECLARION_PLATFORM_URL)")
	}
	ro := requestOptions{tenantID: c.tenantID, tenantCode: c.tenantCode}
	for _, opt := range opts {
		if opt != nil {
			opt(&ro)
		}
	}
	if ro.tenantID != "" && ro.tenantCode != "" {
		return nil, fmt.Errorf("platform client: target tenant id and code are mutually exclusive")
	}

	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(b)
	}

	u := c.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, method, u, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	if err := c.applyHeaders(req, ro); err != nil {
		return nil, err
	}
	return req, nil
}

func (c *Client) applyHeaders(req *http.Request, ro requestOptions) error {
	if c.selectionErr != nil {
		return c.selectionErr
	}
	if c.selected {
		header, err := execution.Encode(c.elevation)
		if err != nil {
			return err
		}
		req.Header.Set(execution.ElevationHeader, header)
	}
	if ro.tenantID != "" && ro.tenantCode != "" {
		return fmt.Errorf("platform client: target tenant id and code are mutually exclusive")
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if ro.tenantID != "" {
		req.Header.Set(TargetTenantIDHeader, ro.tenantID)
	}
	if ro.tenantCode != "" {
		req.Header.Set(TargetTenantCodeHeader, ro.tenantCode)
	}
	setForwardedFor(req, req.Context())
	return nil
}

// do executes an HTTP request with all required headers and buffers the
// response. It returns the CONTENT TYPE beside the body because that is what
// decides whether a non-2xx is the platform speaking: any proxy may answer RFC
// 9457, and a body accepted for its shape alone lets a proxy-owned identity
// pass as a platform one.
//
// A request that never produced a response, and a response this client could
// not read, take the client's OWN transport types - so a caller can ask
// errors.Is(err, errs.ErrRetryable) about a dial failure and be told yes.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body any, opts ...RequestOption) ([]byte, int, string, error) {
	r, err := c.exchange(ctx, method, path, query, body, opts...)
	return r.body, r.status, r.contentType, err
}

// response is one buffered platform response; header is nil when no response
// arrived.
type response struct {
	body        []byte
	status      int
	contentType string
	header      http.Header
}

// exchange is do with the response headers kept, and the one place a buffered
// response passes through, so every one of them reports its parameter version.
func (c *Client) exchange(ctx context.Context, method, path string, query url.Values, body any, opts ...RequestOption) (response, error) {
	req, err := c.newRequest(ctx, method, path, query, body, opts...)
	if err != nil {
		return response{}, err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return response{}, errorFromTransport(path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	processParams.observe(c.baseURL, resp.Header)
	r := response{status: resp.StatusCode, contentType: resp.Header.Get("Content-Type"), header: resp.Header}

	// MaxBytesReader surfaces *http.MaxBytesError on overflow so callers see
	// "response exceeded N bytes" instead of a silently-truncated body that
	// downstream JSON parse would misreport as "unexpected end of JSON input".
	respBody, err := io.ReadAll(http.MaxBytesReader(nil, resp.Body, MaxResponseSize))
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return r, errorFromUnreadable(resp.StatusCode, path,
				fmt.Errorf("response exceeded %d bytes (limit %d)", maxErr.Limit, MaxResponseSize))
		}
		return r, errorFromUnreadable(resp.StatusCode, path, err)
	}
	r.body = respBody
	return r, nil
}
