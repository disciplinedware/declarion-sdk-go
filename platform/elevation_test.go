package platform

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/disciplinedware/declarion-sdk-go/execution"
	"github.com/stretchr/testify/require"
)

func TestClientSelectionPreservesTransportAndSiblings(t *testing.T) {
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"scope":"handler","elevation":{"grants":["original"]}}`))
	transport, err := NewTransport(time.Second)
	require.NoError(t, err)
	httpClient := &http.Client{Transport: transport}
	base := New(Config{BaseURL: "http://example.com", Token: "e30." + payload + ".signature", TargetTenantID: "tenant", HTTPClient: httpClient})
	a, err := base.WithGrants("a")
	require.NoError(t, err)
	b, err := base.WithGrants("b")
	require.NoError(t, err)
	combined, err := a.WithGrants("b")
	require.NoError(t, err)
	system := combined.WithSystemRole()
	for _, tc := range []struct {
		client *Client
		grants []string
		system bool
	}{
		{a, []string{"a", "original"}, false}, {b, []string{"b", "original"}, false},
		{combined, []string{"a", "b", "original"}, false}, {system, []string{"a", "b", "original"}, true},
	} {
		request, err := tc.client.newRequest(t.Context(), "GET", "/api/data/record", nil, nil)
		require.NoError(t, err)
		selected, err := execution.Decode(request.Header.Get(execution.ElevationHeader))
		require.NoError(t, err)
		require.Equal(t, tc.grants, selected.Grants)
		require.Equal(t, tc.system, selected.System)
		require.Same(t, base.http, tc.client.http)
		require.Equal(t, "tenant", request.Header.Get(TargetTenantIDHeader))
		require.Empty(t, request.Header.Get("traceparent"))
	}
	require.Equal(t, []string{"original"}, base.Elevation().Grants)
	request, err := base.newRequest(t.Context(), "GET", "/api/data/record", nil, nil)
	require.NoError(t, err)
	require.Empty(t, request.Header.Get(execution.ElevationHeader))
}

func TestClientSelectionStreamingRequest(t *testing.T) {
	requests := make(chan http.Header, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.Header.Clone()
		w.Header().Set("Content-Type", "text/event-stream")
		_, err := w.Write([]byte(startBlock + dataBlock(`{"result":true}`) + endSuccess))
		require.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	client := New(Config{BaseURL: server.URL, Token: "dk:credential", TargetTenantID: "tenant", HTTPClient: server.Client()})
	selected, err := client.WithGrants("entity:record:read")
	require.NoError(t, err)
	selected = selected.WithSystemRole()
	stream, err := selected.Actions().InvokeStreaming(t.Context(), "record.read", InvokeParams{})
	require.NoError(t, err)
	frames, err := parseStream(stream)
	require.NoError(t, err)
	require.Equal(t, []string{`{"result":true}`}, frames)
	header := <-requests
	elevation, err := execution.Decode(header.Get(execution.ElevationHeader))
	require.NoError(t, err)
	require.Equal(t, selected.Elevation(), elevation)
	require.Equal(t, "Bearer dk:credential", header.Get("Authorization"))
	require.Equal(t, "tenant", header.Get(TargetTenantIDHeader))
	require.Empty(t, header.Get("traceparent"))
	require.Same(t, client.http, selected.http)
}
