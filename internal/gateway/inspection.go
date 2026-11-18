package gateway

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/halfisnine/upstream-gateway/internal/errorrules"
	"github.com/halfisnine/upstream-gateway/internal/interceptlog"
)

const maxErrorBody = 64 << 10
const maxJSONBody = 8 << 20
const maxSSEEvent = 256 << 10
const inspectionWait = 30 * time.Second

// A deadline covers upstream reading only. Cancellation interrupts Transport reads.
type timedReader struct {
	reader io.Reader
	cancel context.CancelFunc
	idle   time.Duration
}

func (t timedReader) Read(b []byte) (int, error) {
	var mu sync.Mutex
	finished := false
	timer := time.AfterFunc(t.idle, func() {
		mu.Lock()
		defer mu.Unlock()
		if !finished {
			t.cancel()
		}
	})
	n, e := t.reader.Read(b)
	mu.Lock()
	finished = true
	timer.Stop()
	mu.Unlock()
	return n, e
}
func decodedBody(resp *http.Response, cancel context.CancelFunc, idle time.Duration) (io.Reader, error) {
	r := io.Reader(timedReader{resp.Body, cancel, idle})
	switch strings.ToLower(resp.Header.Get("Content-Encoding")) {
	case "", "identity":
		return r, nil
	case "gzip":
		return gzip.NewReader(r)
	default:
		return nil, errors.New("unsupported_encoding")
	}
}
func boundedRead(r io.Reader, limit int) ([]byte, error) {
	raw, e := io.ReadAll(io.LimitReader(r, int64(limit)+1))
	if len(raw) > limit {
		return raw[:limit], errors.New("inspection_limit")
	}
	return raw, e
}
func (p *Proxy) handleResponse(w *observedWriter, r *http.Request, resp *http.Response, snap *Snapshot, channel, proto, id string, cancel context.CancelFunc) {
	controller := http.NewResponseController(w)
	defer controller.SetWriteDeadline(time.Time{})
	var original []byte
	var originalPartial bool
	credential, _, _ := requestCredential(r, proto)
	emit := func(source string, fields errorrules.Error, reason string) {
		decision := errorrules.Match(snap.Rules, errorrules.Sample{Channel: channel, Protocol: proto, Source: source, Status: resp.StatusCode, Error: fields})
		if reason != "" {
			decision = errorrules.Decision{Status: 502, Message: errorrules.DefaultMessage, Reason: reason}
			if resp.StatusCode >= 400 && resp.StatusCode <= 599 {
				decision.Status = resp.StatusCode
			}
		}
		committed := w.status != 0
		actual := decision.Status
		action := "safe_response"
		if committed {
			actual = w.status
			action = "safe_sse_error"
		}
		// The excerpt is bounded and encrypted before entering the asynchronous queue.
		eventRecord := interceptlog.Event{ID: RandomID(), Time: time.Now().UTC().Truncate(time.Microsecond), RequestID: id, Node: p.node.ID, Instance: p.node.Instance, Channel: channel, Protocol: proto, Version: snap.Version, RuleID: decision.RuleID, Group: decision.Group, Reason: decision.Reason, Source: source, Upstream: resp.StatusCode, Desired: decision.Status, Actual: actual, Action: action}
		if p.node.Logs != nil {
			eventRecord.CaptureOriginal(p.node.Key, original, credential, originalPartial)
			p.node.Logs.Enqueue(eventRecord)
		}
		_ = controller.SetWriteDeadline(time.Now().Add(p.write))
		if !committed {
			w.Header().Del("Content-Encoding")
			if decision.Status == 429 || decision.Status == 503 {
				if sec, e := strconv.Atoi(resp.Header.Get("Retry-After")); e == nil && sec >= 0 && sec <= 300 {
					w.Header().Set("Retry-After", strconv.Itoa(sec))
				}
			}
			safeMessage(w, proto, decision.Status, id, decision.Message)
			return
		}
		msg := decision.Message + " Reference: " + id
		body := errorBody(proto, decision.Status, msg)
		event := ""
		if proto == "anthropic_messages" {
			event = "event: error\n"
		}
		if proto == "openai_responses" {
			event = "event: error\n"
			body = map[string]any{"type": "error", "code": "gateway_" + strconv.Itoa(decision.Status), "message": msg, "param": nil}
		}
		raw, _ := json.Marshal(body)
		if _, e := w.Write(append([]byte(event+"data: "), append(raw, '\n', '\n')...)); e != nil {
			panic(http.ErrAbortHandler)
		}
		if e := controller.Flush(); e != nil {
			panic(http.ErrAbortHandler)
		}
	}
	if resp.StatusCode != 200 {
		// Limit total wait even if a malicious upstream trickles bytes forever.
		timer := time.AfterFunc(2*time.Second, cancel)
		reader, e := decodedBody(resp, cancel, p.idle)
		var raw []byte
		if e == nil {
			raw, e = boundedRead(reader, maxErrorBody)
		}
		timer.Stop()
		original = raw
		originalPartial = e != nil
		if r.Context().Err() != nil {
			return
		}
		if e != nil {
			emit("http", errorrules.Error{}, "error_body_unreadable")
		} else {
			emit("http", errorrules.HTTPFields(raw, proto), "")
		}
		return
	}
	media, _, e := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	encoding := resp.Header.Get("Content-Encoding")
	blocked := connectionHeaders(resp.Header)
	if e != nil || (media != "application/json" && media != "text/event-stream") || (encoding != "" && encoding != "identity" && encoding != "gzip") || blocked["Content-Type"] || blocked["Content-Encoding"] {
		timer := time.AfterFunc(2*time.Second, cancel)
		reader, readErr := decodedBody(resp, cancel, p.idle)
		if readErr == nil {
			original, readErr = boundedRead(reader, maxErrorBody)
		}
		originalPartial = readErr != nil
		timer.Stop()
		if r.Context().Err() != nil {
			return
		}
		emit("http", errorrules.Error{}, "unsupported_response")
		return
	}
	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Request-ID", id)
	if !snap.Inspect {
		if encoding != "" {
			w.Header().Set("Content-Encoding", encoding)
		}
		p.copyResponse(w, resp.Body, media == "text/event-stream", cancel)
		return
	}
	// Decoding is bounded on decompressed bytes as well as read time.
	timer := time.AfterFunc(inspectionWait, cancel)
	reader, e := decodedBody(resp, cancel, p.idle)
	if e != nil {
		timer.Stop()
		emit("json", errorrules.Error{}, "invalid_encoding")
		return
	}
	if media == "application/json" {
		raw, err := boundedRead(reader, maxJSONBody)
		original = raw
		originalPartial = err != nil
		timer.Stop()
		if r.Context().Err() != nil {
			return
		}
		if err != nil {
			emit("json", errorrules.Error{}, "json_inspection_limit_or_read_failed")
			return
		}
		fields, found, err := errorrules.Extract(raw, proto, "")
		if err != nil {
			emit("json", errorrules.Error{}, "invalid_json")
			return
		}
		if found {
			emit("json", fields, "")
			return
		}
		_ = controller.SetWriteDeadline(time.Now().Add(p.write))
		w.WriteHeader(200)
		if _, err = w.Write(raw); err != nil {
			panic(http.ErrAbortHandler)
		}
		return
	}
	timer.Stop()
	stream := &sseReader{reader: bufio.NewReaderSize(reader, 32<<10), first: true}
	for {
		timer := time.AfterFunc(inspectionWait, cancel)
		raw, event, data, err := stream.next()
		original = raw
		originalPartial = err != nil
		timer.Stop()
		if r.Context().Err() != nil {
			return
		}
		if err == io.EOF {
			if len(raw) > 0 {
				_ = controller.SetWriteDeadline(time.Now().Add(p.write))
				if _, e = w.Write(raw); e != nil {
					panic(http.ErrAbortHandler)
				}
			}
			return
		}
		if err != nil {
			emit("sse", errorrules.Error{}, "sse_inspection_limit_or_read_failed")
			return
		}
		if len(data) > 0 && string(data) != "[DONE]" {
			fields, found, parseErr := errorrules.Extract(data, proto, event)
			if parseErr != nil {
				emit("sse", errorrules.Error{}, "invalid_sse_data")
				return
			}
			if found {
				emit("sse", fields, "")
				return
			}
		} else if event == "error" || event == "response.failed" {
			emit("sse", errorrules.Error{}, "")
			return
		}
		_ = controller.SetWriteDeadline(time.Now().Add(p.write))
		if _, e = w.Write(raw); e != nil {
			panic(http.ErrAbortHandler)
		}
		if e = controller.Flush(); e != nil {
			panic(http.ErrAbortHandler)
		}
	}
}

// Retain exact normal event bytes (including CRLF, comments, IDs and multiline data).
// Incomplete events at EOF are not forwarded as if complete.
type sseReader struct {
	reader        *bufio.Reader
	skipLF, first bool
}

func readSSE(r *bufio.Reader) ([]byte, string, []byte, error) {
	return (&sseReader{reader: r, first: true}).next()
}

// CR, LF and CRLF are all legal SSE line endings. A LF following a CR is
// retained with the next event so a CR-terminated event never waits for a peek.
func (s *sseReader) next() (raw []byte, event string, data []byte, err error) {
	line := []byte{}
	if s.first {
		s.first = false
		prefix, e := s.reader.Peek(3)
		if e == nil && bytes.Equal(prefix, []byte{0xef, 0xbb, 0xbf}) {
			_, _ = s.reader.Discard(3)
			raw = append(raw, prefix...)
		}
	}
	completeLines := false
	for {
		ch, e := s.reader.ReadByte()
		if e != nil {
			if e == io.EOF && len(line) == 0 && !completeLines {
				return raw, "", nil, io.EOF
			}
			return raw, "", nil, io.ErrUnexpectedEOF
		}
		raw = append(raw, ch)
		if len(raw) > maxSSEEvent {
			return raw, "", nil, errors.New("sse_event_limit")
		}
		if s.skipLF {
			s.skipLF = false
			if ch == '\n' {
				continue
			}
		}
		if ch != '\n' && ch != '\r' {
			line = append(line, ch)
			continue
		}
		s.skipLF = ch == '\r'
		if len(line) == 0 {
			return raw, event, bytes.TrimSuffix(data, []byte{'\n'}), nil
		}
		completeLines = true
		field, value, found := bytes.Cut(line, []byte{':'})
		if !found {
			value = nil
		}
		value = bytes.TrimPrefix(value, []byte{' '})
		if string(field) == "event" {
			event = string(value)
		}
		if string(field) == "data" {
			data = append(data, value...)
			data = append(data, '\n')
		}
		line = line[:0]
	}
}
func (p *Proxy) copyResponse(w *observedWriter, body io.Reader, stream bool, cancel context.CancelFunc) {
	controller := http.NewResponseController(w)
	_ = controller.SetWriteDeadline(time.Now().Add(p.write))
	w.WriteHeader(200)
	if stream {
		if err := controller.Flush(); err != nil {
			panic(http.ErrAbortHandler)
		}
	}
	reader := timedReader{body, cancel, p.idle}
	buf := make([]byte, 32<<10)
	for {
		n, e := reader.Read(buf)
		if n > 0 {
			_ = controller.SetWriteDeadline(time.Now().Add(p.write))
			if _, err := w.Write(buf[:n]); err != nil {
				panic(http.ErrAbortHandler)
			}
			if stream {
				if err := controller.Flush(); err != nil {
					panic(http.ErrAbortHandler)
				}
			}
		}
		if e == io.EOF {
			return
		}
		if e != nil {
			panic(http.ErrAbortHandler)
		}
	}
}
