package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/pysugar/oauth-llm-nexus/internal/gateway/codec"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/ir"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/sse"
)

// StreamPolicy tunes hold-before-commit and keepalive behaviour for streaming clients.
type StreamPolicy struct {
	KeepaliveAfter     time.Duration // first keepalive once a request has been held this long
	KeepaliveEvery     time.Duration // keepalive cadence while the client sees no bytes
	AnthropicPingEvery time.Duration // at most one `event: ping` per window (comments in between)
	HoldMax            time.Duration // commit buffered events after this long even without content
	HoldMaxBytes       int           // commit once this many upstream bytes are buffered
}

// DefaultStreamPolicy mirrors the field-tested values used by mature gateways.
var DefaultStreamPolicy = StreamPolicy{
	KeepaliveAfter:     3 * time.Second,
	KeepaliveEvery:     5 * time.Second,
	AnthropicPingEvery: 60 * time.Second,
	HoldMax:            15 * time.Second,
	HoldMaxBytes:       1 << 20,
}

// clientStream wraps the client ResponseWriter of a streaming request. It is shared across
// failover candidates so keepalives can start before a backend is chosen; WriteHeader becomes a
// no-op once headers are out, letting a later candidate stream into the same response.
type clientStream struct {
	http.ResponseWriter
	proto       ir.Protocol
	policy      StreamPolicy
	headersSent bool
	committed   bool // first upstream bytes or events were written
	lastWrite   time.Time
	lastPing    time.Time
}

func newClientStream(w http.ResponseWriter, proto ir.Protocol, p StreamPolicy) *clientStream {
	return &clientStream{ResponseWriter: w, proto: proto, policy: p}
}

func (cs *clientStream) WriteHeader(code int) {
	if cs.headersSent {
		return
	}
	cs.headersSent = true
	cs.ResponseWriter.WriteHeader(code)
}

func (cs *clientStream) Write(b []byte) (int, error) {
	if !cs.headersSent {
		cs.WriteHeader(http.StatusOK)
	}
	cs.lastWrite = time.Now()
	return cs.ResponseWriter.Write(b)
}

func (cs *clientStream) Flush() {
	if f, ok := cs.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// ensureHeaders sends SSE headers and 200 before the backend is known.
func (cs *clientStream) ensureHeaders() {
	if cs.headersSent {
		return
	}
	sse.SetHeaders(cs)
	cs.Header().Set("X-Nexus-Route", "held")
	cs.WriteHeader(http.StatusOK)
	cs.lastWrite = time.Now()
}

// beat writes a protocol-appropriate keepalive. Gemini SDKs fail on comment lines, so Gemini
// clients get nothing (and headers are not committed for them).
func (cs *clientStream) beat() {
	if cs.proto == ir.ProtoGemini {
		return
	}
	cs.ensureHeaders()
	sw := sse.NewWriter(cs)
	now := time.Now()
	if cs.proto == ir.ProtoAnthropic && now.Sub(cs.lastPing) >= cs.policy.AnthropicPingEvery {
		cs.lastPing = now
		_ = sw.WritePing()
		return
	}
	_ = sw.WriteCommentLine("keepalive")
}

// maybeBeat sends a keepalive when the client has been quiet for KeepaliveEvery.
func (cs *clientStream) maybeBeat(heldSince time.Time) {
	if cs == nil {
		return
	}
	now := time.Now()
	if now.Sub(heldSince) < cs.policy.KeepaliveAfter {
		return
	}
	if cs.lastWrite.IsZero() || now.Sub(cs.lastWrite) >= cs.policy.KeepaliveEvery {
		cs.beat()
	}
}

// writeStreamError reports ge inside an already-open stream using the client's own protocol.
func (cs *clientStream) writeStreamError(c codec.Codec, ge *ir.GatewayError) {
	sw := sse.NewWriter(cs)
	enc := c.NewEncoder("")
	_ = enc.Encode(ir.Event{Kind: ir.EventError, Err: ge}, sw)
}

type pumpItem struct {
	ev  sse.Event
	err error
}

// framePump reads SSE frames on its own goroutine so the consumer can interleave keepalives,
// hold deadlines and first-token timeouts. Close (or closing the body) ends the goroutine.
type framePump struct {
	ch   chan pumpItem
	stop chan struct{}
	once sync.Once
}

func startPump(sr *sse.Reader) *framePump {
	p := &framePump{ch: make(chan pumpItem, 16), stop: make(chan struct{})}
	go func() {
		for {
			ev, err := sr.Next()
			select {
			case p.ch <- pumpItem{ev: ev, err: err}:
			case <-p.stop:
				return
			}
			if err != nil {
				return
			}
		}
	}()
	return p
}

func (p *framePump) Close() { p.once.Do(func() { close(p.stop) }) }

// heldFrame is one upstream frame with the IR events it decoded to.
type heldFrame struct {
	raw    []byte
	events []ir.Event
}

// streamReader drives a decoder over a pump with idle, hold and first-token timers.
type streamReader struct {
	ctx      context.Context
	pump     *framePump
	dec      codec.StreamDecoder
	cs       *clientStream // nil for non-streaming clients
	idle     time.Duration // no upstream frame for this long aborts the attempt
	ftt      time.Duration // first-token timeout (0 = none)
	policy   StreamPolicy
	started  time.Time
	ttft     time.Duration
	eof      bool
	done     bool
	content  bool
	finished string // last finish reason seen
}

func isContentEvent(ev ir.Event) bool {
	switch ev.Kind {
	case ir.EventText, ir.EventThought:
		return ev.Text != ""
	case ir.EventToolCallStart:
		return true
	}
	return false
}

// next returns the next decoded frame. io.EOF means the stream is over (check Complete).
func (sr *streamReader) next(pre bool, holdUntil time.Time) (heldFrame, error) {
	tick := time.NewTicker(sr.tickEvery())
	defer tick.Stop()
	idle := time.NewTimer(sr.idleOrDefault())
	defer idle.Stop()
	var fttC <-chan time.Time
	if pre && sr.ftt > 0 {
		t := time.NewTimer(time.Until(sr.started.Add(sr.ftt)))
		defer t.Stop()
		fttC = t.C
	}
	var holdC <-chan time.Time
	if pre && !holdUntil.IsZero() {
		t := time.NewTimer(time.Until(holdUntil))
		defer t.Stop()
		holdC = t.C
	}
	for {
		select {
		case it := <-sr.pump.ch:
			if it.err != nil {
				if errors.Is(it.err, io.EOF) {
					sr.eof = true
					return heldFrame{}, io.EOF
				}
				return heldFrame{}, it.err
			}
			events, done, ge := sr.dec.DecodeFrame(it.ev)
			if ge != nil {
				return heldFrame{}, ge
			}
			for _, ev := range events {
				if isContentEvent(ev) && !sr.content {
					sr.content = true
					sr.ttft = time.Since(sr.started)
				}
				if ev.Kind == ir.EventFinish {
					sr.finished = ev.FinishReason
				}
			}
			if done {
				sr.done = true
			}
			return heldFrame{raw: it.ev.Raw, events: events}, nil
		case <-tick.C:
			if sr.cs != nil {
				sr.cs.maybeBeat(sr.started)
			}
		case <-idle.C:
			return heldFrame{}, ir.NewError(504, "upstream_idle_timeout",
				"upstream sent nothing for "+sr.idleOrDefault().String())
		case <-fttC:
			return heldFrame{}, ir.NewError(504, "first_token_timeout",
				"no first token within "+sr.ftt.String())
		case <-holdC:
			return heldFrame{}, errHoldExpired
		case <-sr.ctx.Done():
			return heldFrame{}, ir.NewError(499, "request_canceled", "client canceled request")
		}
	}
}

var errHoldExpired = errors.New("hold window expired")

// tickEvery is the keepalive check cadence: half the shorter keepalive interval, within [50ms, 1s].
func (sr *streamReader) tickEvery() time.Duration {
	d := sr.policy.KeepaliveEvery
	if sr.policy.KeepaliveAfter > 0 && sr.policy.KeepaliveAfter < d {
		d = sr.policy.KeepaliveAfter
	}
	d /= 2
	if d < 50*time.Millisecond {
		d = 50 * time.Millisecond
	}
	if d > time.Second {
		d = time.Second
	}
	return d
}

func (sr *streamReader) idleOrDefault() time.Duration {
	if sr.idle > 0 {
		return sr.idle
	}
	return 180 * time.Second
}

func (s *Server) streamPolicy() StreamPolicy {
	if s.Stream != nil {
		return *s.Stream
	}
	return DefaultStreamPolicy
}

// toGatewayError converts pump/read errors into gateway errors.
func toGatewayError(err error) *ir.GatewayError {
	var ge *ir.GatewayError
	if errors.As(err, &ge) {
		return ge
	}
	return ir.NewError(502, "upstream_stream_read_error", err.Error())
}

func errIncomplete() *ir.GatewayError {
	return ir.NewError(502, "upstream_incomplete", "upstream stream ended before the reply was complete")
}

func errEmptyReply() *ir.GatewayError {
	return ir.NewError(502, "empty_reply", "upstream finished without producing any content")
}

func errFinishError() *ir.GatewayError {
	return ir.NewError(502, "upstream_finish_error", "upstream finished the reply with an error")
}

// hold reads frames until the first content (text, thinking or a tool call), buffering them so a
// broken or empty attempt can still fail over. It returns commit=true when the buffered frames
// may be sent, or a pre-commit error. finished=true means the stream already ended cleanly.
func (sr *streamReader) hold() (frames []heldFrame, finished bool, ge *ir.GatewayError) {
	holdUntil := sr.started.Add(sr.policy.HoldMax)
	if sr.ftt > sr.policy.HoldMax {
		holdUntil = sr.started.Add(sr.ftt)
	}
	held := 0
	for {
		f, err := sr.next(true, holdUntil)
		if err != nil {
			if errors.Is(err, errHoldExpired) {
				return frames, false, nil
			}
			if errors.Is(err, io.EOF) {
				return sr.finishHeld(frames)
			}
			return nil, false, toGatewayError(err)
		}
		frames = append(frames, f)
		held += len(f.raw)
		if sr.finished == ir.FinishError {
			return nil, false, errFinishError()
		}
		if sr.done {
			return sr.finishHeld(frames)
		}
		if sr.content || held >= sr.policy.HoldMaxBytes {
			return frames, false, nil
		}
	}
}

// finishHeld validates a stream that ended while still held and appends its trailing events.
func (sr *streamReader) finishHeld(frames []heldFrame) ([]heldFrame, bool, *ir.GatewayError) {
	var tail []ir.Event
	if ge := sr.drainEnd(func(ev ir.Event) { tail = append(tail, ev) }); ge != nil {
		return nil, false, ge
	}
	if len(tail) > 0 {
		frames = append(frames, heldFrame{events: tail})
	}
	return frames, true, nil
}

// drainEnd finishes a stream that stopped (done or EOF): it rejects truncated, errored or empty
// replies, and otherwise passes the decoder's trailing events (pending tool calls, finish) to emit.
func (sr *streamReader) drainEnd(emit func(ir.Event)) *ir.GatewayError {
	if !sr.dec.Complete() {
		return errIncomplete()
	}
	tail := sr.dec.Flush()
	for _, ev := range tail {
		if isContentEvent(ev) {
			sr.content = true
		}
		if ev.Kind == ir.EventFinish && ev.FinishReason != "" {
			sr.finished = ev.FinishReason
		}
	}
	if sr.finished == ir.FinishError {
		return errFinishError()
	}
	if !sr.content {
		return errEmptyReply()
	}
	for _, ev := range tail {
		emit(ev)
	}
	return nil
}

// respContext returns the context of the request that produced resp (Background if unknown).
func respContext(resp *http.Response) context.Context {
	if resp != nil && resp.Request != nil {
		return resp.Request.Context()
	}
	return context.Background()
}
