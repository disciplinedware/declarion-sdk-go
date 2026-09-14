package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	mcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCPClient connects to Declarion's typed Model Context Protocol endpoint.
// A session is scoped to the bearer in its parent Client.
type MCPClient struct{ c *Client }

// MCPTool is a tool visible to the authenticated platform caller.
type MCPTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// MCPContent is a protocol content item returned by a tool call.
// Raw preserves all MCP content variants without losing data to a client-side
// projection.
type MCPContent struct {
	Raw json.RawMessage `json:"raw"`
}

// MCPCallResult is the typed result of a platform tool call.
type MCPCallResult struct {
	Content           []MCPContent    `json:"content"`
	StructuredContent json.RawMessage `json:"structured_content,omitempty"`
	IsError           bool            `json:"is_error"`
}

// MCPSession is an initialized MCP connection. Close it after the turn that
// owns its bearer has completed.
type MCPSession struct{ session *mcp.ClientSession }

// Connect opens an authenticated, request-response-only MCP session.
func (c *MCPClient) Connect(ctx context.Context) (*MCPSession, error) {
	if c == nil || c.c == nil {
		return nil, fmt.Errorf("platform MCP client is not configured")
	}
	httpClient := *c.c.http
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	httpClient.Transport = mcpTransport{base: c.c.http.Transport, platform: c.c}
	transport := &mcp.StreamableClientTransport{
		Endpoint:             c.c.baseURL + "/api/mcp",
		HTTPClient:           &httpClient,
		MaxRetries:           -1,
		DisableStandaloneSSE: true,
	}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "declarion-sdk-go", Version: "1"}, nil).Connect(ctx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("platform MCP connect: %w", err)
	}
	return &MCPSession{session: session}, nil
}

// ListTools returns the complete caller-specific tool catalog.
func (s *MCPSession) ListTools(ctx context.Context) ([]MCPTool, error) {
	if s == nil || s.session == nil {
		return nil, fmt.Errorf("platform MCP session is not configured")
	}
	var tools []MCPTool
	for cursor := ""; ; {
		result, err := s.session.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor})
		if err != nil {
			return nil, fmt.Errorf("platform MCP list tools: %w", err)
		}
		for _, tool := range result.Tools {
			if tool == nil || tool.Name == "" {
				return nil, fmt.Errorf("platform MCP list tools: invalid tool")
			}
			schema, err := json.Marshal(tool.InputSchema)
			if err != nil {
				return nil, fmt.Errorf("platform MCP list tools: encode schema for %q: %w", tool.Name, err)
			}
			tools = append(tools, MCPTool{Name: tool.Name, Description: tool.Description, InputSchema: schema})
		}
		if result.NextCursor == "" {
			return tools, nil
		}
		if result.NextCursor == cursor {
			return nil, fmt.Errorf("platform MCP list tools: repeated cursor")
		}
		cursor = result.NextCursor
	}
}

// CallTool invokes one tool under the session's bearer.
func (s *MCPSession) CallTool(ctx context.Context, name string, arguments any) (*MCPCallResult, error) {
	if s == nil || s.session == nil {
		return nil, fmt.Errorf("platform MCP session is not configured")
	}
	if name == "" {
		return nil, fmt.Errorf("platform MCP call tool: name is required")
	}
	result, err := s.session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		return nil, fmt.Errorf("platform MCP call tool: %w", err)
	}
	converted := &MCPCallResult{IsError: result.IsError}
	for _, item := range result.Content {
		raw, err := json.Marshal(item)
		if err != nil {
			return nil, fmt.Errorf("platform MCP call tool: encode content: %w", err)
		}
		converted.Content = append(converted.Content, MCPContent{Raw: raw})
	}
	if result.StructuredContent != nil {
		raw, err := json.Marshal(result.StructuredContent)
		if err != nil {
			return nil, fmt.Errorf("platform MCP call tool: encode structured content: %w", err)
		}
		converted.StructuredContent = raw
	}
	return converted, nil
}

// Close closes the MCP session.
func (s *MCPSession) Close() error {
	if s == nil || s.session == nil {
		return nil
	}
	return s.session.Close()
}

type mcpTransport struct {
	base     http.RoundTripper
	platform *Client
}

func (t mcpTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.platform == nil {
		return nil, fmt.Errorf("platform MCP transport is not configured")
	}
	endpoint, err := url.Parse(t.platform.baseURL + "/api/mcp")
	if err != nil || req.URL.Scheme != endpoint.Scheme || req.URL.Host != endpoint.Host || req.URL.Path != endpoint.Path {
		return nil, fmt.Errorf("platform MCP request destination is outside the platform endpoint")
	}
	cloned := req.Clone(req.Context())
	if err := t.platform.applyHeaders(cloned, requestOptions{tenantID: t.platform.tenantID, tenantCode: t.platform.tenantCode}); err != nil {
		return nil, err
	}
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(cloned)
}
