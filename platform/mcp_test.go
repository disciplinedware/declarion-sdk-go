package platform

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	mcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPClientUsesPlatformAuthorizationForCatalogAndCall(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	server.AddTool(&mcp.Tool{Name: "orders.place", Description: "Place an order", InputSchema: map[string]any{"type": "object"}}, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var arguments struct {
			Symbol string `json:"symbol"`
		}
		if err := json.Unmarshal(req.Params.Arguments, &arguments); err != nil || arguments.Symbol != "SPY" {
			t.Errorf("arguments = %s, err = %v", req.Params.Arguments, err)
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "accepted"}}}, nil
	})
	var mu sync.Mutex
	requests := 0
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer turn-bearer" {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("traceparent") != "00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01" {
			t.Errorf("traceparent = %q", r.Header.Get("traceparent"))
		}
		if r.Header.Get(TargetTenantIDHeader) != "tenant-1" {
			t.Errorf("tenant = %q", r.Header.Get(TargetTenantIDHeader))
		}
		mu.Lock()
		requests++
		mu.Unlock()
		mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true}).ServeHTTP(w, r)
	}))
	defer httpServer.Close()

	client := New(Config{
		BaseURL:        httpServer.URL,
		Token:          "turn-bearer",
		Traceparent:    "00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01",
		TargetTenantID: "tenant-1",
	})
	session, err := client.MCP().Connect(context.Background())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = session.Close() }()
	tools, err := session.ListTools(context.Background())
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "orders.place" || string(tools[0].InputSchema) != `{"type":"object"}` {
		t.Fatalf("tools = %#v", tools)
	}
	result, err := session.CallTool(context.Background(), "orders.place", map[string]any{"symbol": "SPY"})
	if err != nil {
		t.Fatalf("call tool: %v", err)
	}
	if result.IsError || len(result.Content) != 1 {
		t.Fatalf("result = %#v", result)
	}
	var content struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(result.Content[0].Raw, &content); err != nil || content.Type != "text" || content.Text != "accepted" {
		t.Fatalf("content = %#v, err = %v", content, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if requests < 3 {
		t.Fatalf("requests = %d, want initialization, list, and call", requests)
	}
}

func TestMCPClientDoesNotForwardBearerOnRedirect(t *testing.T) {
	requests := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
	}))
	defer target.Close()
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer platform.Close()

	client := New(Config{BaseURL: platform.URL, Token: "turn-bearer"})
	if _, err := client.MCP().Connect(context.Background()); err == nil {
		t.Fatal("redirected MCP connection succeeded")
	}
	if requests != 0 {
		t.Fatalf("redirect target received %d requests", requests)
	}
}
