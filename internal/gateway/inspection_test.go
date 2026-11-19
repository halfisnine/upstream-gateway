package gateway

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/halfisnine/upstream-gateway/internal/config"
	"github.com/halfisnine/upstream-gateway/internal/errorrules"
	"github.com/halfisnine/upstream-gateway/internal/interceptlog"
)

type eventSink struct {
	mu     sync.Mutex
	events []interceptlog.Event
}

func (s *eventSink) WriteEvents(_ context.Context, events []interceptlog.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, events...)
	return nil
}
func (s *eventSink) PruneEvents(context.Context) error { return nil }
func inspectSetup(t *testing.T, h http.HandlerFunc) (*Node, *httptest.Server, *eventSink) {
	up := httptest.NewServer(h)
	t.Cleanup(up.Close)
	n := New(nil, bytes.Repeat([]byte{3}, 32), "test")
	s := &eventSink{}
	n.Logs = interceptlog.New(s)
	c := testConfig(up.URL)
	c.LegacyRouting = false
	c.InspectionTemplates = config.InspectionPresets()
	for _, template := range c.InspectionTemplates[:4] {
		c.Channels[0].InspectionTemplates = append(c.Channels[0].InspectionTemplates, template.ID)
	}
	c.InspectEmbeddedErrors = true
	c.ErrorRules = errorrules.Presets()
	if e := n.Activate(c, 7, "test"); e != nil {
		t.Fatal(e)
	}
	gw := httptest.NewServer(n.Handler())
	t.Cleanup(n.Close)
	t.Cleanup(gw.Close)
	return n, gw, s
}
func TestConfiguredErrorsAcrossProtocols(t *testing.T) {
	for _, path := range []string{"/v1/chat/completions", "/v1/responses", "/v1/messages", "/v1beta/models/test:generateContent"} {
		for _, status := range []int{200, 403} {
			for _, compressed := range []bool{false, true} {
				t.Run(path+http.StatusText(status)+map[bool]string{true: "gzip", false: "plain"}[compressed], func(t *testing.T) {
					var hits atomic.Int32
					n, gw, sink := inspectSetup(t, func(w http.ResponseWriter, r *http.Request) {
						hits.Add(1)
						w.Header().Set("Content-Type", "application/json")
						if compressed {
							w.Header().Set("Content-Encoding", "gzip")
						}
						w.WriteHeader(status)
						raw := `{"error":{"code":"insufficient_quota","message":"PRIVATE_ACCOUNT"}}`
						if compressed {
							z := gzip.NewWriter(w)
							io.WriteString(z, raw)
							z.Close()
						} else {
							io.WriteString(w, raw)
						}
					})
					res, body := call(t, request(t, gw.URL, path))
					if res.StatusCode != 503 || strings.Contains(body, "PRIVATE_ACCOUNT") || !strings.Contains(body, "上游服务暂时不可用") || !json.Valid([]byte(body)) || hits.Load() != 1 {
						t.Fatal(res.StatusCode, body, hits.Load())
					}
					n.Logs.Close()
					if len(sink.events) != 1 {
						t.Fatal(sink.events)
					}
					event := sink.events[0]
					original, openErr := event.OpenOriginal(n.Key)
					if openErr != nil || !strings.Contains(original, "PRIVATE_ACCOUNT") {
						t.Fatal("original error not retained", openErr)
					}
					raw, _ := json.Marshal(event)
					if event.Version != 7 || event.RuleID != "balance" || event.Actual != 503 || strings.Contains(string(raw), "PRIVATE_ACCOUNT") {
						t.Fatal(event)
					}
				})
			}
		}
	}
}
func TestNormalJSONPreservedWithInspection(t *testing.T) {
	raw := `{"choices":[{"message":{"content":"余额不足 insufficient_quota"}}],"usage":{"total_tokens":5,"cache_read_input_tokens":2}}`
	_, gw, _ := inspectSetup(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, raw)
	})
	res, body := call(t, request(t, gw.URL, "/v1/chat/completions"))
	if res.StatusCode != 200 || body != raw {
		t.Fatal(res.StatusCode, body)
	}
}
func TestSSEMidstreamUsesOriginalSnapshot(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	n, gw, sink := inspectSetup(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"余额不足\"}}]}\r\n\r\n")
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		for _, chunk := range []string{"event: er", "ror\r\ndata: {\"error\":\n", "data: {\"code\":\"insufficient_quota\",\"message\":\"PRIVATE\"}}\r\n\r\n"} {
			io.WriteString(w, chunk)
			w.(http.Flusher).Flush()
		}
	})
	res, e := http.DefaultClient.Do(request(t, gw.URL, "/v1/chat/completions"))
	if e != nil {
		t.Fatal(e)
	}
	defer res.Body.Close()
	reader := bufio.NewReader(res.Body)
	first, e := reader.ReadString('\n')
	if e != nil || !strings.Contains(first, "余额不足") {
		t.Fatal(first, e)
	}
	old := n.Current()
	c := testConfig(old.Channels["a"].BaseURL)
	c.InspectEmbeddedErrors = true
	c.ErrorRules = errorrules.Presets()
	c.ErrorRules[0].Message = "NEW_VERSION"
	if e = n.Activate(c, 8, "next"); e != nil {
		t.Fatal(e)
	}
	release <- struct{}{}
	rest, e := io.ReadAll(reader)
	if e != nil {
		t.Fatal(e)
	}
	if res.StatusCode != 200 || strings.Contains(string(rest), "PRIVATE") || strings.Contains(string(rest), "NEW_VERSION") || strings.Contains(string(rest), "[DONE]") || !strings.Contains(string(rest), "上游服务暂时不可用") {
		t.Fatal(string(rest))
	}
	n.Logs.Close()
	if len(sink.events) == 1 {
		original, err := sink.events[0].OpenOriginal(n.Key)
		if err != nil || !strings.Contains(original, "PRIVATE") || strings.Contains(original, "choices") {
			t.Fatal("SSE original must contain only failing event", err)
		}
	}
	if len(sink.events) != 1 || sink.events[0].Version != 7 || sink.events[0].Actual != 200 || sink.events[0].Desired != 503 || sink.events[0].Action != "safe_sse_error" {
		t.Fatal(sink.events)
	}
}
func TestSSEFirstErrorCanChangeHTTPStatus(t *testing.T) {
	_, gw, _ := inspectSetup(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "event: error\ndata: {\"error\":{\"message\":\"余额不足\"}}\n\n")
	})
	res, body := call(t, request(t, gw.URL, "/v1/messages"))
	if res.StatusCode != 503 || strings.Contains(body, "余额不足") {
		t.Fatal(res.StatusCode, body)
	}
}
func TestInspectionFailsClosedOnMalformedAndOversized(t *testing.T) {
	for _, tc := range []struct{ media, body string }{{"application/json", "{"}, {"application/json", strings.Repeat("x", maxJSONBody+1)}, {"text/event-stream", "data: " + strings.Repeat("x", maxSSEEvent)}, {"text/event-stream", "data: {\"error\":\"PRIVATE\"}"}, {"text/event-stream", "data: INVALID_PRIVATE\n\n"}} {
		_, gw, _ := inspectSetup(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", tc.media)
			io.WriteString(w, tc.body)
		})
		res, body := call(t, request(t, gw.URL, "/v1/chat/completions"))
		if res.StatusCode != 502 || strings.Contains(body, "PRIVATE") {
			t.Fatal(res.StatusCode, body)
		}
	}
}
func TestSSEFramingPreservesBytes(t *testing.T) {
	raw := []byte(": ping\r\nevent: message\r\nid: 1\r\ndata: {\"x\":\r\ndata: 1}\r\n\r\n")
	got, event, data, e := readSSE(bufio.NewReader(bytes.NewReader(raw)))
	if e != nil || !bytes.Equal(got, raw[:len(raw)-1]) || event != "message" || string(data) != "{\"x\":\n1}" {
		t.Fatal(string(got), event, string(data), e)
	}
}
func TestInspectionIdleTimeout(t *testing.T) {
	n, gw, _ := inspectSetup(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	n.proxy.idle = 20 * time.Millisecond
	res, _ := call(t, request(t, gw.URL, "/v1/chat/completions"))
	if res.StatusCode != 502 {
		t.Fatal(res.StatusCode)
	}
}

func TestSSECRAndBOMErrorRecognition(t *testing.T) {
	_, gw, _ := inspectSetup(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "\xef\xbb\xbfevent: error\rdata: {\"error\":{\"message\":\"余额不足\"}}\r\r")
	})
	res, body := call(t, request(t, gw.URL, "/v1/chat/completions"))
	if res.StatusCode != 503 || strings.Contains(body, "余额不足") {
		t.Fatal(res.StatusCode, body)
	}
}
func TestSSECRLFNormalBytesPreserved(t *testing.T) {
	raw := "\xef\xbb\xbf: ping\r\ndata: {\"choices\":[]}\r\n\r\ndata: [DONE]\r\n\r\n"
	_, gw, _ := inspectSetup(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, raw)
	})
	res, body := call(t, request(t, gw.URL, "/v1/chat/completions"))
	if res.StatusCode != 200 || body != raw {
		t.Fatalf("%d %q", res.StatusCode, body)
	}
}

func TestOriginalAvailableForParseFailure(t *testing.T) {
	for _, body := range []string{`{"error":broken`, strings.Repeat("x", maxJSONBody+1)} {
		n, gw, sink := inspectSetup(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, body)
		})
		res, _ := call(t, request(t, gw.URL, "/v1/chat/completions"))
		if res.StatusCode != 502 {
			t.Fatal(res.StatusCode)
		}
		n.Logs.Close()
		original, e := sink.events[0].OpenOriginal(n.Key)
		if e != nil || len(original) == 0 {
			t.Fatal("missing rejected body", e)
		}
		if len(body) > maxJSONBody && !sink.events[0].OriginalTruncated {
			t.Fatal("unmarked prefix")
		}
	}
}
