package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/halfisnine/upstream-gateway/internal/config"
)

var geminiPath = regexp.MustCompile(`^/v1(beta)?/models/[A-Za-z0-9._-]+:(generateContent|streamGenerateContent)$`)

type Proxy struct {
	node               *Node
	transport          *http.Transport
	total, idle, write time.Duration
}

type observedWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (w *observedWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *observedWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
func (w *observedWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	return n, err
}

func NewProxy(n *Node) *Proxy {
	return &Proxy{node: n, transport: &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext, ForceAttemptHTTP2: true, MaxIdleConns: 128, MaxIdleConnsPerHost: 8, MaxConnsPerHost: 16, IdleConnTimeout: 90 * time.Second, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 180 * time.Second, MaxResponseHeaderBytes: 32 << 10, DisableCompression: true}, total: 1800 * time.Second, idle: 180 * time.Second, write: 30 * time.Second}
}
func (n *Node) Handler() http.Handler { return n.proxy }
func protocol(path string) string {
	switch path {
	case "/v1/chat/completions":
		return "openai_chat"
	case "/v1/responses":
		return "openai_responses"
	case "/v1/messages":
		return "anthropic_messages"
	}
	if geminiPath.MatchString(path) {
		return "gemini"
	}
	return ""
}
func errorType(status int) string {
	switch status {
	case 400:
		return "invalid_request_error"
	case 401:
		return "authentication_error"
	case 403:
		return "permission_error"
	case 404:
		return "not_found_error"
	case 413:
		return "request_too_large"
	case 429:
		return "rate_limit_error"
	case 529:
		return "overloaded_error"
	default:
		return "api_error"
	}
}
func safeError(w http.ResponseWriter, p string, status int, id string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Request-ID", id)
	msg := "Request could not be completed. Reference: " + id
	var body any
	switch p {
	case "anthropic_messages":
		body = map[string]any{"type": "error", "error": map[string]any{"type": errorType(status), "message": msg}}
	case "gemini":
		st := "UNKNOWN"
		switch status {
		case 400:
			st = "INVALID_ARGUMENT"
		case 401:
			st = "UNAUTHENTICATED"
		case 403:
			st = "PERMISSION_DENIED"
		case 404:
			st = "NOT_FOUND"
		case 429:
			st = "RESOURCE_EXHAUSTED"
		case 500:
			st = "INTERNAL"
		case 503:
			st = "UNAVAILABLE"
		case 504:
			st = "DEADLINE_EXCEEDED"
		}
		body = map[string]any{"error": map[string]any{"code": status, "message": msg, "status": st}}
	default:
		body = map[string]any{"error": map[string]any{"type": errorType(status), "message": msg, "code": "gateway_" + strconv.Itoa(status)}}
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
func validQuery(r *http.Request, p string) bool {
	q, e := url.ParseQuery(r.URL.RawQuery)
	if e != nil {
		return false
	}
	for k, v := range q {
		if len(v) != 1 {
			return false
		}
		if p == "gemini" && strings.HasSuffix(r.URL.Path, ":streamGenerateContent") && k == "alt" && v[0] == "sse" {
			continue
		}
		if p == "anthropic_messages" && k == "beta" && v[0] == "true" {
			continue
		}
		return false
	}
	return true
}
func authHeader(auth string) string {
	switch auth {
	case "x_api_key":
		return "X-Api-Key"
	case "x_goog_api_key":
		return "X-Goog-Api-Key"
	default:
		return "Authorization"
	}
}

// requestCredential only checks the carrier and syntax. The upstream validates
// the actual key; it is never compared with, or saved in, gateway configuration.
func requestCredential(r *http.Request, p string) (string, string, bool) {
	header := authHeader(config.DefaultAuth(p))
	for _, h := range []string{"Authorization", "X-Api-Key", "X-Goog-Api-Key"} {
		vs := r.Header.Values(h)
		if (h != header && len(vs) != 0) || (h == header && len(vs) != 1) {
			return "", "", false
		}
	}
	if connectionHeaders(r.Header)[header] {
		return "", "", false
	}
	value := r.Header.Get(header)
	key := value
	if header == "Authorization" {
		scheme, rest, found := strings.Cut(value, " ")
		if !found || !strings.EqualFold(scheme, "Bearer") {
			return "", "", false
		}
		key = rest
	}
	if len(key) == 0 || len(key) > 8192 {
		return "", "", false
	}
	for _, ch := range key {
		if ch < 33 || ch > 126 || ch == ',' {
			return "", "", false
		}
	}
	return key, value, true
}
func connectionHeaders(h http.Header) map[string]bool {
	out := map[string]bool{}
	for _, v := range h.Values("Connection") {
		for _, s := range strings.Split(v, ",") {
			out[http.CanonicalHeaderKey(strings.TrimSpace(s))] = true
		}
	}
	return out
}
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	id := "gw_" + RandomID()
	started := time.Now()
	observed := &observedWriter{ResponseWriter: w}
	w = observed
	var applied int64
	defer func() {
		cause := recover()
		outcome := "http_completed"
		if cause != nil {
			outcome = "stream_aborted"
		}
		if r.Context().Err() != nil {
			outcome = "client_canceled"
		}
		slog.Info("model request finished", "request_id", id, "config_version", applied,
			"status", observed.status, "bytes_out", observed.bytes,
			"duration_ms", time.Since(started).Milliseconds(), "outcome", outcome)
		if cause != nil {
			panic(cause)
		}
	}()
	fail := func(proto string, status int) { safeError(w, proto, status, id) }
	// Never clean or redirect malformed paths. Reject escaped delimiters and ambiguous decoding.
	if strings.Contains(r.URL.EscapedPath(), "%") || strings.ContainsAny(r.URL.Path, "\\") || strings.Contains(r.URL.Path, "//") {
		fail("", 400)
		return
	}
	for _, seg := range strings.Split(r.URL.Path, "/") {
		if seg == "." || seg == ".." {
			fail("", 400)
			return
		}
	}
	parts := strings.SplitN(r.URL.Path, "/", 4)
	if len(parts) != 4 || parts[1] != "r" || !config.ValidID(parts[2]) {
		fail("", 404)
		return
	}
	path := "/" + parts[3]
	proto := protocol(path)
	if proto == "" {
		fail("", 404)
		return
	}
	snap := p.node.Current()
	if snap == nil || !p.node.Ready() {
		fail(proto, 503)
		return
	}
	applied = snap.Version
	ch, ok := snap.Channels[parts[2]]
	if !ok {
		fail(proto, 404)
		return
	}
	if _, ok = ch.Protocols[proto]; !ok {
		fail(proto, 404)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		fail(proto, 405)
		return
	}
	if !validQuery(r, proto) {
		fail(proto, 400)
		return
	}
	key, originalCredential, validCredential := requestCredential(r, proto)
	if !validCredential {
		fail(proto, 401)
		return
	}
	typ, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if e != nil || typ != "application/json" || len(r.Header.Values("Content-Type")) != 1 || r.Header.Get("Content-Encoding") != "" {
		fail(proto, 400)
		return
	}
	if r.ContentLength > 16<<20 {
		fail(proto, 413)
		return
	}
	select {
	case p.node.slots <- struct{}{}:
		defer func() { <-p.node.slots }()
	default:
		fail(proto, 503)
		return
	}
	p.node.active.Add(1)
	defer p.node.active.Add(-1)
	ctx, cancel := context.WithTimeout(r.Context(), p.total)
	defer cancel()
	eff := ch.Effective(proto)
	target, _ := url.Parse(eff.BaseURL)
	target.Path = strings.TrimSuffix(target.Path, "/") + path
	target.RawPath = ""
	target.RawQuery = r.URL.RawQuery
	// MaxBytesReader bounds streaming uploads; unknown-size requests stay non-replayable.
	body := http.MaxBytesReader(w, r.Body, 16<<20)
	out, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), body)
	if err != nil {
		fail(proto, 502)
		return
	}
	out.ContentLength = r.ContentLength
	out.GetBody = nil
	hop := connectionHeaders(r.Header)
	for _, h := range []string{"Content-Type", "Accept"} {
		if !hop[h] {
			out.Header.Set(h, r.Header.Get(h))
		}
	}
	if out.Header.Get("Content-Type") == "" {
		fail(proto, 400)
		return
	}
	if proto == "anthropic_messages" {
		for _, h := range []string{"Anthropic-Version", "Anthropic-Beta"} {
			if len(r.Header.Values(h)) > 1 || len(r.Header.Get(h)) > 1024 {
				fail(proto, 400)
				return
			}
			if v := r.Header.Get(h); v != "" && !hop[h] {
				out.Header.Set(h, v)
			}
		}
	}
	out.Header.Set("Accept-Encoding", "identity")
	h := authHeader(eff.Auth)
	credential := key
	if h == "Authorization" {
		credential = "Bearer " + credential
	}
	if eff.Auth == config.DefaultAuth(proto) {
		credential = originalCredential
	}
	out.Header.Set(h, credential)
	resp, err := p.transport.RoundTrip(out)
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		var limit *http.MaxBytesError
		var ne net.Error
		status := 502
		if errors.As(err, &limit) {
			status = 413
		} else if ctx.Err() != nil || (errors.As(err, &ne) && ne.Timeout()) {
			status = 504
		}
		fail(proto, status)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		status := resp.StatusCode
		if status < 400 || status > 599 {
			status = 502
		}
		if status == 429 || status == 503 {
			if sec, e := strconv.Atoi(resp.Header.Get("Retry-After")); e == nil && sec >= 0 && sec <= 300 {
				w.Header().Set("Retry-After", strconv.Itoa(sec))
			}
		}
		fail(proto, status)
		return
	}
	media, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	encoding := resp.Header.Get("Content-Encoding")
	blocked := connectionHeaders(resp.Header)
	if err != nil || (media != "application/json" && media != "text/event-stream") || (encoding != "" && encoding != "identity" && encoding != "gzip") || blocked["Content-Type"] || blocked["Content-Encoding"] {
		fail(proto, 502)
		return
	}
	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Request-ID", id)
	if encoding != "" {
		w.Header().Set("Content-Encoding", encoding)
	}
	controller := http.NewResponseController(w)
	_ = controller.SetWriteDeadline(time.Now().Add(p.write))
	w.WriteHeader(200)
	if media == "text/event-stream" {
		if err = controller.Flush(); err != nil {
			panic(http.ErrAbortHandler)
		}
	}
	buf := make([]byte, 32<<10)
	for {
		// The timer only covers upstream reads, not downstream backpressure.
		var mu sync.Mutex
		finished := false
		timer := time.AfterFunc(p.idle, func() {
			mu.Lock()
			defer mu.Unlock()
			if !finished {
				cancel()
			}
		})
		count, readErr := resp.Body.Read(buf)
		mu.Lock()
		finished = true
		timer.Stop()
		mu.Unlock()
		if count > 0 {
			_ = controller.SetWriteDeadline(time.Now().Add(p.write))
			if _, err = w.Write(buf[:count]); err != nil {
				panic(http.ErrAbortHandler)
			}
			if media == "text/event-stream" {
				if err = controller.Flush(); err != nil {
					panic(http.ErrAbortHandler)
				}
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			panic(http.ErrAbortHandler)
		}
	}
	_ = controller.SetWriteDeadline(time.Time{})
}
