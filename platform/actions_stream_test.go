package platform

import (
	"github.com/disciplinedware/declarion-sdk-go/errs"

	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// --- SSE wire-block builders (must mirror declarion-core's httpStreamSink) ---

const startBlock = "event: declarion.stream.start\ndata: {\"model\":\"x\"}\n\n"
const endSuccess = "event: declarion.stream.end\ndata: {\"status\":\"success\"}\n\n"

func dataBlock(payload string) string {
	var b strings.Builder
	for _, line := range strings.Split(payload, "\n") {
		b.WriteString("data: ")
		b.WriteString(line)
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	return b.String()
}

// sseServer serves one fixed SSE body (flushed) at 200 text/event-stream.
func sseServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, body)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func streamClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	return New(Config{BaseURL: srv.URL})
}

// parseStream drives an ActionStream to completion and returns the frames.
func parseStream(s *ActionStream) ([]string, error) {
	var frames []string
	for s.Next() {
		frames = append(frames, string(s.Data()))
	}
	return frames, s.Err()
}

// --- InvokeStreaming (HTTP-level) ---

func TestInvokeStreaming_HappyPath(t *testing.T) {
	body := startBlock + dataBlock("frame-0") + dataBlock("frame-1") + dataBlock("frame-2") + endSuccess
	c := streamClient(t, sseServer(t, body))

	s, err := c.Actions().InvokeStreaming(context.Background(), "e2e.stream_echo", InvokeParams{Args: map[string]any{"stream": true}})
	if err != nil {
		t.Fatalf("InvokeStreaming: %v", err)
	}
	defer func() { _ = s.Close() }()

	if got := string(s.Meta()); got != `{"model":"x"}` {
		t.Errorf("meta: got %q, want the start metadata", got)
	}
	frames, err := parseStream(s)
	if err != nil {
		t.Fatalf("stream Err: %v", err)
	}
	want := []string{"frame-0", "frame-1", "frame-2"}
	if strings.Join(frames, ",") != strings.Join(want, ",") {
		t.Errorf("frames: got %v, want %v", frames, want)
	}
}

func TestInvokeStreaming_PreStartNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"type":"/errors/action.failed","title":"nope","retryable":false}`)
	}))
	t.Cleanup(srv.Close)

	_, err := streamClient(t, srv).Actions().InvokeStreaming(context.Background(), "x", InvokeParams{})
	apiErr, ok := errs.From(err)
	if !ok {
		t.Fatalf("expected *errs.Error, got %T: %v", err, err)
	}
	if status, _ := StatusOf(apiErr); status != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", apiErr.Status)
	}
}

func TestInvokeStreaming_WrongContentType(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"status":"success"}`)
	}))
	t.Cleanup(srv.Close)

	_, err := streamClient(t, srv).Actions().InvokeStreaming(context.Background(), "x", InvokeParams{})
	if err == nil || !strings.Contains(err.Error(), "text/event-stream") {
		t.Fatalf("expected content-type error, got %v", err)
	}
}

func TestInvokeStreaming_FirstEventNotStart(t *testing.T) {
	c := streamClient(t, sseServer(t, dataBlock("frame-0")+endSuccess))
	_, err := c.Actions().InvokeStreaming(context.Background(), "x", InvokeParams{})
	if err == nil || !strings.Contains(err.Error(), "first event") {
		t.Fatalf("expected first-event error, got %v", err)
	}
}

func TestInvokeStreaming_TerminalError(t *testing.T) {
	end := "event: declarion.stream.end\ndata: {\"status\":\"error\"," +
		"\"error\":{\"type\":\"/errors/action.failed\",\"title\":\"kaboom\",\"retryable\":false}," +
		"\"delivered_frames\":1,\"delivered_bytes\":7}\n\n"
	c := streamClient(t, sseServer(t, startBlock+dataBlock("frame-0")+end))
	s, err := c.Actions().InvokeStreaming(context.Background(), "x", InvokeParams{})
	if err != nil {
		t.Fatalf("InvokeStreaming: %v", err)
	}
	defer func() { _ = s.Close() }()

	frames, streamErr := parseStream(s)
	if len(frames) != 1 || frames[0] != "frame-0" {
		t.Errorf("frames before error: got %v, want [frame-0]", frames)
	}
	se, ok := errs.From(streamErr)
	if !ok {
		t.Fatalf("expected *errs.Error, got %T: %v", streamErr, streamErr)
	}
	if se.Code() != "action.failed" || se.Title != "kaboom" {
		t.Errorf("stream error: got %+v", se)
	}
	// A partially delivered answer is not a failed one. The counts come from
	// the terminal event, not from counting locally - the server's count is the
	// one that describes what it sent.
	if got := s.Delivered(); got.Frames != 1 || got.Bytes != 7 {
		t.Errorf("delivered: got %+v, want {Frames:1 Bytes:7}", got)
	}
}

// --- Parser-level (white-box) ---

func newParserStream(body string, maxEventBytes int) *ActionStream {
	return newActionStream(io.NopCloser(strings.NewReader(body)), nil, maxEventBytes)
}

func TestParser_MultilineDataPreservesLF(t *testing.T) {
	s := newParserStream(startBlock+dataBlock("a\nb\nc")+endSuccess, MaxStreamEventSize)
	if err := s.readStart(); err != nil {
		t.Fatalf("readStart: %v", err)
	}
	frames, err := parseStream(s)
	if err != nil {
		t.Fatalf("Err: %v", err)
	}
	if len(frames) != 1 || frames[0] != "a\nb\nc" {
		t.Errorf("multiline frame: got %q, want %q", frames, "a\nb\nc")
	}
}

func TestParser_HeartbeatIgnored(t *testing.T) {
	body := startBlock + ": heartbeat\n\n" + dataBlock("frame-0") + ": heartbeat\n\n" + endSuccess
	s := newParserStream(body, MaxStreamEventSize)
	if err := s.readStart(); err != nil {
		t.Fatalf("readStart: %v", err)
	}
	frames, err := parseStream(s)
	if err != nil {
		t.Fatalf("Err: %v", err)
	}
	if len(frames) != 1 || frames[0] != "frame-0" {
		t.Errorf("heartbeats must be skipped: got %v", frames)
	}
}

func TestParser_FailClosedCases(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		cap     int
		wantErr string
	}{
		{"cr_rejected", startBlock + "data: bad\rframe\n\n" + endSuccess, MaxStreamEventSize, "CR"},
		{"invalid_utf8", startBlock + "data: \xff\xfe\n\n" + endSuccess, MaxStreamEventSize, "UTF-8"},
		{"oversize_event", startBlock + dataBlock(strings.Repeat("x", 200)) + endSuccess, 64, "exceeds"},
		{"unknown_control", startBlock + "event: declarion.stream.middle\ndata: {}\n\n" + endSuccess, MaxStreamEventSize, "unknown control"},
		{"second_start", startBlock + startBlock + endSuccess, MaxStreamEventSize, "second start"},
		{"eof_before_terminal", startBlock + dataBlock("frame-0"), MaxStreamEventSize, "EOF before terminal"},
		{"unknown_terminal_status", startBlock + "event: declarion.stream.end\ndata: {\"status\":\"weird\"}\n\n", MaxStreamEventSize, "unknown terminal status"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newParserStream(tc.body, tc.cap)
			if err := s.readStart(); err != nil {
				t.Fatalf("readStart: %v", err)
			}
			_, err := parseStream(s)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("got err %v, want containing %q", err, tc.wantErr)
			}
		})
	}
}

// countingReader tracks how many bytes were pulled from the underlying
// reader, so a test can assert the parser aborted early instead of buffering
// an entire oversize, delimiter-free line before checking the cap.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// TestParser_OversizeUnterminatedLineFailsClosed guards the bounded-buffering
// contract: bufio.Reader.ReadString/ReadBytes have no internal size cap, so a
// single SSE line with no trailing '\n' (a non-conformant or hostile
// endpoint) would be read fully into memory - arbitrarily past
// maxEventBytes - before size-checking could ever fire. The parser must
// enforce the cap against the running total after every internal read
// fragment, not only once '\n' or EOF is found, and it must fail with the
// size-cap error rather than an "EOF mid-event" error.
func TestParser_OversizeUnterminatedLineFailsClosed(t *testing.T) {
	const cap = 64
	const lineSize = 10 * 1024 * 1024 // far larger than cap; no trailing '\n' anywhere
	unterminated := "data: " + strings.Repeat("x", lineSize)

	cr := &countingReader{r: strings.NewReader(startBlock + unterminated)}
	s := newActionStream(io.NopCloser(cr), nil, cap)
	if err := s.readStart(); err != nil {
		t.Fatalf("readStart: %v", err)
	}
	_, err := parseStream(s)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("got err %v, want containing %q (not an EOF error)", err, "exceeds")
	}
	if strings.Contains(err.Error(), "EOF") {
		t.Fatalf("got err %v, want the size-cap error, not an EOF-mid-event error", err)
	}
	// Bounded buffering: the parser must abort within roughly one internal
	// read-buffer's worth of bytes past the cap, never anywhere close to the
	// full 10MiB unterminated line.
	const maxAllowedRead = 1 << 20 // 1MiB slack, far below lineSize
	if cr.n > maxAllowedRead {
		t.Fatalf("parser pulled %d bytes from the source past a %d-byte cap; want bounded reads, not the whole unterminated line", cr.n, cap)
	}
}

func TestParser_StartMustBeValidJSON(t *testing.T) {
	s := newParserStream("event: declarion.stream.start\ndata: not-json\n\n"+endSuccess, MaxStreamEventSize)
	if err := s.readStart(); err == nil || !strings.Contains(err.Error(), "valid JSON") {
		t.Fatalf("expected start-json error, got %v", err)
	}
}

// --- Close / cancellation ---

// blockingSSEServer sends start + one frame, then blocks until release is
// closed, holding the stream open so the client's next read stalls.
func blockingSSEServer(t *testing.T) (*httptest.Server, chan struct{}) {
	t.Helper()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		f, _ := w.(http.Flusher)
		_, _ = io.WriteString(w, startBlock+dataBlock("frame-0"))
		if f != nil {
			f.Flush()
		}
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() {
		close(release)
		srv.Close()
	})
	return srv, release
}

func TestActionStream_CloseIsIdempotentAndUnblocks(t *testing.T) {
	srv, _ := blockingSSEServer(t)
	s, err := streamClient(t, srv).Actions().InvokeStreaming(context.Background(), "x", InvokeParams{})
	if err != nil {
		t.Fatalf("InvokeStreaming: %v", err)
	}

	if !s.Next() || string(s.Data()) != "frame-0" {
		t.Fatalf("expected frame-0, got %q err=%v", s.Data(), s.Err())
	}

	// A blocked Next in a goroutine must unblock when Close is called.
	done := make(chan bool, 1)
	go func() { done <- s.Next() }()
	time.Sleep(50 * time.Millisecond)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case got := <-done:
		if got {
			t.Errorf("Next after Close must be false")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Next did not unblock after Close")
	}
	if err := s.Close(); err != nil {
		t.Errorf("second Close must be nil, got %v", err)
	}
}

func TestActionStream_ContextCancellation(t *testing.T) {
	srv, _ := blockingSSEServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	s, err := streamClient(t, srv).Actions().InvokeStreaming(ctx, "x", InvokeParams{})
	if err != nil {
		t.Fatalf("InvokeStreaming: %v", err)
	}
	defer func() { _ = s.Close() }()

	if !s.Next() {
		t.Fatalf("expected first frame, err=%v", s.Err())
	}
	cancel()
	if s.Next() {
		t.Fatalf("Next must be false after cancellation")
	}
	if !errors.Is(s.Err(), context.Canceled) {
		t.Errorf("Err must be context.Canceled, got %v", s.Err())
	}
}
