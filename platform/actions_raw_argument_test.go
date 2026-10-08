package platform

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const orderedArgument = `{"zeta":{"y":{"b":"1","B":2,"a":[{"d":1e2,"c":1.0}]}},"reasoning":"r","score":12345678901234567890}`

func TestActionsInvokeSendsRawArgumentVerbatim(t *testing.T) {
	received := make(chan string, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		received <- string(raw)
		if strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, startBlock+endSuccess)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success"}`))
	}))
	t.Cleanup(srv.Close)
	c := New(Config{BaseURL: srv.URL, HTTPClient: srv.Client()})
	params := InvokeParams{Args: map[string]any{"body": json.RawMessage(orderedArgument), "a": 1}}

	if _, err := c.Actions().Invoke(t.Context(), "test.action", params); err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	stream, err := c.Actions().InvokeStreaming(t.Context(), "test.action", params)
	if err != nil {
		t.Fatalf("InvokeStreaming: %v", err)
	}
	if _, err := parseStream(stream); err != nil {
		t.Fatalf("stream: %v", err)
	}
	_ = stream.Close()

	for _, call := range []string{"Invoke", "InvokeStreaming"} {
		body := <-received
		if !strings.Contains(body, `"body":`+orderedArgument) {
			t.Errorf("%s sent %s, want the argument verbatim: %s", call, body, orderedArgument)
		}
	}
}
