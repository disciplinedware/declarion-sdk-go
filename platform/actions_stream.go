package platform

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"unicode/utf8"
)

// Streaming action support for /api/actions/{code} (declarion-core plan
// 2026-07-22-streaming-actions).
//
// A dual-mode action selected with `stream: true` responds with an SSE stream
// framed by the platform: exactly one declarion.stream.start control event
// carrying JSON metadata, zero or more ordinary data events, and exactly one
// terminal declarion.stream.end control event ({"status":"success"} or
// {"status":"error",...}). This client validates that contract and hands the
// caller RAW frame bytes - it never interprets provider payloads, so native
// provider SSE (OpenAI, Anthropic) passes through unchanged.

// Reserved SSE control-event names. Must match declarion-core's httpStreamSink.
const (
	streamEventStart = "declarion.stream.start"
	streamEventEnd   = "declarion.stream.end"
)

// MaxStreamEventSize bounds a single SSE event the client will buffer. The
// parser holds at most one event plus fixed state, so this is the client's
// worst-case retained stream memory. An event exceeding it fails the stream
// rather than growing memory unboundedly. 100MB matches the platform's
// DefaultHandlerResponseLimit (see platform/client.go's MaxResponseSize) so a
// single event the platform was willing to send always fits here.
const MaxStreamEventSize = 100 * 1024 * 1024

// errEventTooLarge is the sentinel wrapped by readLine's size-cap error, so
// readEvent can distinguish "capped" from "read failed" without string
// matching.
var errEventTooLarge = errors.New("streaming action: event exceeds max size")

// StreamError is the typed terminal error carried by a declarion.stream.end
// error event - a failure AFTER the stream committed HTTP 200. A failure BEFORE
// the stream started surfaces as *APIError from InvokeStreaming instead.
type StreamError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details any    `json:"details,omitempty"`
}

func (e *StreamError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("stream error %s: %s", e.Code, e.Message)
	}
	return fmt.Sprintf("stream error %s", e.Code)
}

// streamEndEnvelope is the terminal declarion.stream.end JSON body.
type streamEndEnvelope struct {
	Status string       `json:"status"`
	Error  *StreamError `json:"error,omitempty"`
}

// sseEvent is one parsed SSE event: an empty Event means an ordinary data frame.
type sseEvent struct {
	event string
	data  []byte
}

// ActionStream is an incremental reader over a streaming action response. Usage:
//
//	s, err := client.Actions().InvokeStreaming(ctx, code, params)
//	if err != nil { ... } // pre-start failure (*APIError) or start-validation error
//	defer s.Close()
//	for s.Next() {
//	    frame := s.Data() // raw UTF-8 payload bytes
//	}
//	if err := s.Err(); err != nil { ... } // post-start terminal error (*StreamError) or parse error
//
// ActionStream is NOT safe for concurrent use. Close is idempotent and cancels
// the underlying request.
type ActionStream struct {
	meta   json.RawMessage
	body   io.ReadCloser
	reader *bufio.Reader
	cancel context.CancelFunc

	maxEventBytes int
	cur           []byte
	err           error
	done          bool
	// closed is atomic because Close may be called from another goroutine to
	// unblock a stalled Next (the idiomatic streaming-reader contract). Every
	// other field is owned by the single Next-calling goroutine.
	closed atomic.Bool
}

// newActionStream wraps a response body with the SSE parser. Shared by
// InvokeStreaming and by white-box parser tests (which pass a lower cap).
func newActionStream(body io.ReadCloser, cancel context.CancelFunc, maxEventBytes int) *ActionStream {
	return &ActionStream{
		body:          body,
		reader:        bufio.NewReader(body),
		cancel:        cancel,
		maxEventBytes: maxEventBytes,
	}
}

// NewActionStreamStarted wraps an already-open, already-validated (2xx,
// text/event-stream) response body with the shared SSE parser and consumes +
// validates the mandatory start event before returning - the same contract
// InvokeStreaming applies to declarion-core's action route. Exported so other
// SDK packages that reach a dual-mode stream through a non-Actions transport
// (e.g. platform/llmconnector, whose transport is a consumer-owned verified
// wrapper rather than /api/actions/{code}) reuse this parser instead of
// duplicating it. Behavior is identical to InvokeStreaming's own start
// handling; this does not change that behavior, only exposes it.
func NewActionStreamStarted(body io.ReadCloser, cancel context.CancelFunc, maxEventBytes int) (*ActionStream, error) {
	s := newActionStream(body, cancel, maxEventBytes)
	if err := s.readStart(); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

// InvokeStreaming calls POST /api/actions/{code} in streaming mode. It returns
// only AFTER the mandatory start event is received and validated, so a caller
// that gets a non-nil stream already has the start metadata. A pre-start non-2xx
// response is returned as *APIError; a response that is not an event-stream, or
// whose first event is not the start event, is a contract error.
func (a *ActionsClient) InvokeStreaming(ctx context.Context, code string, params InvokeParams) (*ActionStream, error) {
	body := make(map[string]any, len(params.Args)+1)
	for k, v := range params.Args {
		body[k] = v
	}
	if params.IDs != nil {
		body["object_ids"] = params.IDs
	}

	path := fmt.Sprintf("/api/actions/%s", code)
	// The stream is request-cancellable: Close cancels it, which unblocks a
	// stalled read and tears down the connection.
	streamCtx, cancel := context.WithCancel(ctx)
	req, err := a.c.newRequest(streamCtx, "POST", path, nil, body, targetTenantOptions(params.TargetTenantID, params.TargetTenantCode)...)
	if err != nil {
		cancel()
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")

	resp, err := a.c.http.Do(req)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("request POST %s: %w", path, err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Pre-start failure: HTTP status is authoritative, mapped to the same
		// APIError the buffered Invoke returns.
		raw, _ := io.ReadAll(http.MaxBytesReader(nil, resp.Body, MaxResponseSize))
		_ = resp.Body.Close()
		cancel()
		return nil, &APIError{StatusCode: resp.StatusCode, Body: string(raw), Path: path}
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		_ = resp.Body.Close()
		cancel()
		return nil, fmt.Errorf("streaming action %s: expected text/event-stream, got %q", code, ct)
	}

	return NewActionStreamStarted(resp.Body, cancel, MaxStreamEventSize)
}

// readStart consumes events until the first non-heartbeat event, which MUST be
// the start control event, and stores its metadata.
func (s *ActionStream) readStart() error {
	ev, err := s.readEvent()
	if err != nil {
		return err
	}
	if ev.event != streamEventStart {
		return fmt.Errorf("streaming action: expected %s as the first event, got %q", streamEventStart, ev.event)
	}
	if !json.Valid(ev.data) {
		return fmt.Errorf("streaming action: start metadata is not valid JSON")
	}
	s.meta = append(json.RawMessage(nil), ev.data...)
	return nil
}

// Meta returns the immutable start-event metadata as raw JSON. Valid for the
// stream's whole lifetime.
func (s *ActionStream) Meta() json.RawMessage { return s.meta }

// Next advances to the next data frame. It returns true when a frame is
// available (read it with Data), and false when the stream ended - cleanly (Err
// == nil) or with a terminal/parse error (Err != nil).
func (s *ActionStream) Next() bool {
	if s.done || s.closed.Load() {
		return false
	}
	ev, err := s.readEvent()
	if err != nil {
		s.finish(err)
		return false
	}
	switch ev.event {
	case "":
		s.cur = ev.data
		return true
	case streamEventEnd:
		s.finishTerminal(ev.data)
		return false
	case streamEventStart:
		s.finish(errors.New("streaming action: unexpected second start event"))
		return false
	default:
		s.finish(fmt.Errorf("streaming action: unknown control event %q", ev.event))
		return false
	}
}

// Data returns the current data frame's raw bytes (valid until the next Next).
func (s *ActionStream) Data() []byte { return s.cur }

// Err returns the terminal error: nil on a clean success end, *StreamError on a
// post-start terminal error event, or a parse/transport error otherwise.
// Meaningful only after Next returns false.
func (s *ActionStream) Err() error { return s.err }

// Close cancels the request and releases the body. Idempotent and safe to call
// from another goroutine to unblock a stalled Next; it touches only the atomic
// closed flag, the goroutine-safe context cancel, and the body (whose Close
// unblocks an in-flight read, per net/http).
func (s *ActionStream) Close() error {
	if !s.closed.CompareAndSwap(false, true) {
		return nil
	}
	if s.cancel != nil {
		s.cancel()
	}
	if s.body != nil {
		return s.body.Close()
	}
	return nil
}

// finish records a terminal error and marks the stream done.
func (s *ActionStream) finish(err error) {
	s.err = err
	s.done = true
}

// finishTerminal decodes the declarion.stream.end envelope: success -> clean
// end (Err nil); error -> *StreamError.
func (s *ActionStream) finishTerminal(data []byte) {
	var env streamEndEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		s.finish(fmt.Errorf("streaming action: malformed terminal event: %w", err))
		return
	}
	switch env.Status {
	case "success":
		s.finish(nil)
	case "error":
		if env.Error == nil {
			s.finish(errors.New("streaming action: terminal error event missing error object"))
			return
		}
		s.finish(env.Error)
	default:
		s.finish(fmt.Errorf("streaming action: unknown terminal status %q", env.Status))
	}
}

// readLine reads one line up to and including its trailing '\n', accumulating
// fragments via bufio.Reader.ReadSlice so the cap is enforced against the
// RUNNING total after every fragment - not only once a delimiter (or EOF) is
// found. ReadString/ReadBytes have no such cap: fed an unterminated line, they
// accumulate fragments unboundedly in memory until '\n' or EOF/error, which is
// exactly the DoS this guards against. *total is the running per-event byte
// count across all lines already read in the current event; readLine updates
// it in place and aborts the moment it would exceed s.maxEventBytes.
func (s *ActionStream) readLine(total *int) ([]byte, error) {
	var line []byte
	for {
		frag, err := s.reader.ReadSlice('\n')
		if len(frag) > 0 {
			*total += len(frag)
			if *total > s.maxEventBytes {
				return nil, fmt.Errorf("%w: exceeds %d bytes", errEventTooLarge, s.maxEventBytes)
			}
			// ReadSlice's returned slice aliases the bufio.Reader's internal
			// buffer and is invalidated by the next Read call, so it must be
			// copied (via append) before looping.
			line = append(line, frag...)
		}
		if err == nil {
			return line, nil
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		return line, err
	}
}

// readEvent reads and parses one SSE event, skipping comment (heartbeat) blocks.
// It enforces the per-event byte cap, rejects CR and invalid UTF-8 data, joins
// multiline data with LF, and strips one leading space after `data:`.
func (s *ActionStream) readEvent() (sseEvent, error) {
	for {
		var event string
		var data []byte
		haveData := false
		comment := false
		total := 0

		for {
			raw, err := s.readLine(&total)
			if err != nil {
				if errors.Is(err, errEventTooLarge) {
					return sseEvent{}, err
				}
				if err == io.EOF {
					if len(raw) == 0 && !haveData && event == "" && !comment && total == 0 {
						return sseEvent{}, fmt.Errorf("streaming action: EOF before terminal event")
					}
					return sseEvent{}, fmt.Errorf("streaming action: EOF mid-event before terminal")
				}
				return sseEvent{}, fmt.Errorf("streaming action: read: %w", err)
			}
			if bytes.IndexByte(raw, '\r') >= 0 {
				return sseEvent{}, fmt.Errorf("streaming action: CR byte in stream is rejected")
			}
			// Strip the trailing LF; a bare "\n" line terminates the event.
			line := string(bytes.TrimSuffix(raw, []byte("\n")))
			if line == "" {
				break
			}
			switch {
			case strings.HasPrefix(line, ":"):
				// SSE comment (heartbeat). A block that is only comments is
				// skipped; a comment interleaved with fields is ignored.
				comment = true
			case strings.HasPrefix(line, "event:"):
				event = strings.TrimPrefix(strings.TrimPrefix(line, "event:"), " ")
			case strings.HasPrefix(line, "data:"):
				v := strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " ")
				if haveData {
					data = append(data, '\n')
				}
				data = append(data, v...)
				haveData = true
			default:
				return sseEvent{}, fmt.Errorf("streaming action: unrecognized SSE line %q", line)
			}
		}

		if !haveData && event == "" {
			// A pure heartbeat / empty block: skip and read the next event.
			if comment {
				continue
			}
			return sseEvent{}, fmt.Errorf("streaming action: empty SSE event")
		}
		if haveData && !utf8.Valid(data) {
			return sseEvent{}, fmt.Errorf("streaming action: data frame is not valid UTF-8")
		}
		return sseEvent{event: event, data: data}, nil
	}
}
