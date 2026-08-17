package platform

import (
	"github.com/disciplinedware/declarion-sdk-go/errs"

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

// errEventTooLarge marks readLine's size-cap refusal, so readEvent can tell
// "capped" from "read failed" without string matching. It never reaches a
// consumer: readEvent turns it into the declared type.
var errEventTooLarge = errors.New("event exceeds max size")

// streamEndEnvelope is the terminal declarion.stream.end JSON body. The error
// is the same object every other carrier sends, so one parser reads them all -
// and `delivered_frames` / `delivered_bytes` say how much of the answer arrived
// before it stopped, which is the fact a reader of a truncated stream needs and
// cannot otherwise have.
type streamEndEnvelope struct {
	Status          string      `json:"status"`
	Error           *errs.Error `json:"error,omitempty"`
	DeliveredFrames int         `json:"delivered_frames,omitempty"`
	DeliveredBytes  int64       `json:"delivered_bytes,omitempty"`
}

// Delivered is how much of the answer reached this reader before the stream
// ended, from the terminal event rather than counted locally: the two would
// disagree the moment a frame was dropped between them, and the server's count
// is the one that describes what it sent. Zero until the stream is done.
type Delivered struct {
	Frames int
	Bytes  int64
}

// sseEvent is one parsed SSE event: an empty Event means an ordinary data frame.
type sseEvent struct {
	event string
	data  []byte
}

// ActionStream is an incremental reader over a streaming action response. Usage:
//
//	s, err := client.Actions().InvokeStreaming(ctx, code, params)
//	if err != nil { ... } // pre-start failure: the platform's own error object
//	defer s.Close()
//	for s.Next() {
//	    frame := s.Data() // raw UTF-8 payload bytes
//	}
//	if err := s.Err(); err != nil { ... } // post-start terminal error, same object
//
// ActionStream is NOT safe for concurrent use. Close is idempotent and cancels
// the underlying request.
type ActionStream struct {
	meta   json.RawMessage
	body   io.ReadCloser
	reader *bufio.Reader
	cancel context.CancelFunc

	path          string
	maxEventBytes int
	cur           []byte
	// received counts what THIS reader actually took off the wire. The
	// terminal event says what the server sent, and a stream that breaks has
	// no terminal - so a count that came only from there reports zero after a
	// partial answer, and a caller cannot tell "nothing arrived" from "some
	// did".
	received Delivered
	err           error
	done          bool
	delivered     Delivered
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

// WithPath names the route this stream is reading, so a failure it raises says
// which one broke.
func (s *ActionStream) WithPath(path string) *ActionStream {
	s.path = path
	return s
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
		return nil, errorFromTransport(path, err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Pre-start failure: the stream never committed, so this is an ordinary
		// buffered failure and reads through the same classifier.
		raw, _ := io.ReadAll(http.MaxBytesReader(nil, resp.Body, MaxResponseSize))
		contentType := resp.Header.Get("Content-Type")
		_ = resp.Body.Close()
		cancel()
		return nil, errorFromResponse(resp.StatusCode, raw, path, contentType)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		_ = resp.Body.Close()
		cancel()
		return nil, streamUnreadable(path, fmt.Errorf("expected text/event-stream, got %q", ct))
	}

	stream, serr := NewActionStreamStarted(resp.Body, cancel, MaxStreamEventSize)
	if stream != nil {
		stream.WithPath(path)
	}
	return stream, serr
}

// readStart consumes events until the first non-heartbeat event, which MUST be
// the start control event, and stores its metadata.
func (s *ActionStream) readStart() error {
	ev, err := s.readEvent()
	if err != nil {
		return err
	}
	if ev.event != streamEventStart {
		return streamUnreadable(s.path, fmt.Errorf("expected %s as the first event, got %q", streamEventStart, ev.event))
	}
	if !json.Valid(ev.data) {
		return streamUnreadable(s.path, errors.New("start metadata is not valid JSON"))
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
		s.received.Frames++
		s.received.Bytes += int64(len(ev.data))
		return true
	case streamEventEnd:
		s.finishTerminal(ev.data)
		return false
	case streamEventStart:
		s.finish(streamUnreadable(s.path, errors.New("a second start event")))
		return false
	default:
		s.finish(streamUnreadable(s.path, fmt.Errorf("unknown control event %q", ev.event)))
		return false
	}
}

// Data returns the current data frame's raw bytes (valid until the next Next).
func (s *ActionStream) Data() []byte { return s.cur }

// Err returns the terminal error: nil on a clean success end, the action's own
// *errs.Error on a post-start terminal error event, or a parse/transport error
// otherwise. Meaningful only after Next returns false.
func (s *ActionStream) Err() error { return s.err }

// Delivered reports what actually reached this reader. A partially delivered
// answer is not a failed one, and a caller that cannot tell them apart either
// discards work that arrived or trusts an answer that did not finish.
//
// Counted HERE, not taken from the terminal event: a stream that breaks mid-way
// never sends one. The terminal's own counts are what the SERVER sent, which is
// a different fact - Sent() answers that, and the two disagreeing is exactly
// how a caller learns the answer was cut short.
func (s *ActionStream) Delivered() Delivered { return s.received }

// Sent reports what the terminal event said the server delivered. Zero when the
// stream ended without one.
func (s *ActionStream) Sent() Delivered { return s.delivered }

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

// finishTerminal decodes the declarion.stream.end event: success leaves Err
// nil, a failure sets the error object the terminal carried.
func (s *ActionStream) finishTerminal(data []byte) {
	var env streamEndEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		s.finish(streamUnreadable(s.path, fmt.Errorf("malformed terminal event: %w", err)))
		return
	}
	s.delivered = Delivered{Frames: env.DeliveredFrames, Bytes: env.DeliveredBytes}
	switch env.Status {
	case "success":
		s.finish(nil)
	case "error":
		if env.Error == nil {
			s.finish(streamUnreadable(s.path, errors.New("terminal error event carries no error object")))
			return
		}
		s.finish(env.Error)
	default:
		s.finish(streamUnreadable(s.path, fmt.Errorf("unknown terminal status %q", env.Status)))
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
					return sseEvent{}, transportErr(TypeStreamEventTooLarge, errs.Args{
						FieldLimitBytes: s.maxEventBytes,
						FieldPath:       s.path,
					}, err)
				}
				// Every one of these is a stream that ended without its
				// terminal event, which is one fact with one type. The cause
				// says which shape it took and never reaches a wire.
				if errors.Is(err, io.EOF) {
					if len(raw) == 0 && !haveData && event == "" && !comment && total == 0 {
						return sseEvent{}, errorFromInterruptedStream(s.path,
							errors.New("EOF before the terminal event"))
					}
					return sseEvent{}, errorFromInterruptedStream(s.path,
						errors.New("EOF mid-event before the terminal event"))
				}
				return sseEvent{}, errorFromInterruptedStream(s.path, err)
			}
			if bytes.IndexByte(raw, '\r') >= 0 {
				return sseEvent{}, streamUnreadable(s.path, errors.New("a CR byte, which SSE does not allow here"))
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
				return sseEvent{}, streamUnreadable(s.path, fmt.Errorf("unrecognized SSE line %q", line))
			}
		}

		if !haveData && event == "" {
			// A pure heartbeat / empty block: skip and read the next event.
			if comment {
				continue
			}
			return sseEvent{}, streamUnreadable(s.path, errors.New("an empty SSE event"))
		}
		if haveData && !utf8.Valid(data) {
			return sseEvent{}, streamUnreadable(s.path, errors.New("a data frame that is not valid UTF-8"))
		}
		return sseEvent{event: event, data: data}, nil
	}
}
