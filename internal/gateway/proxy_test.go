package gateway

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/halfisnine/upstream-gateway/internal/config"
)

func testConfig(base string) config.Config {
	ps := map[string]config.Endpoint{}
	for _, p := range config.Protocols {
		ps[p] = config.Endpoint{}
	}
	return config.Config{Schema: config.Schema, Channels: []config.Channel{{ID: "a", Name: "A", BaseURL: base, AllowHTTP: true, Protocols: ps}}}
}
func setup(t *testing.T, h http.HandlerFunc) (*Node, *httptest.Server) {
	t.Helper()
	up := httptest.NewServer(h)
	t.Cleanup(up.Close)
	n := New(nil, nil, "test")
	if e := n.Activate(testConfig(up.URL), 1, "test"); e != nil {
		t.Fatal(e)
	}
	gw := httptest.NewServer(n.Handler())
	t.Cleanup(gw.Close)
	t.Cleanup(n.Close)
	return n, gw
}
func request(t *testing.T, base, path string) *http.Request {
	t.Helper()
	r, e := http.NewRequest("POST", base+"/r/a"+path, strings.NewReader(`{"stream":true}`))
	if e != nil {
		t.Fatal(e)
	}
	r.Header.Set("Content-Type", "application/json")
	p := protocol(strings.Split(path, "?")[0])
	h := authHeader(config.DefaultAuth(p))
	key := "upstream-test-key"
	if h == "Authorization" {
		key = "Bearer " + key
	}
	r.Header.Set(h, key)
	return r
}
func call(t *testing.T, r *http.Request) (*http.Response, string) {
	t.Helper()
	res, e := http.DefaultClient.Do(r)
	if e != nil {
		t.Fatal(e)
	}
	b, e := io.ReadAll(res.Body)
	res.Body.Close()
	if e != nil {
		t.Fatal(e)
	}
	return res, string(b)
}
func TestFourProtocolsAndHeaderIsolation(t *testing.T) {
	for _, path := range []string{"/v1/chat/completions", "/v1/responses", "/v1/messages?beta=true", "/v1beta/models/gemini-test:generateContent", "/v1/models/gemini-test:streamGenerateContent?alt=sse"} {
		t.Run(path, func(t *testing.T) {
			p := protocol(strings.Split(path, "?")[0])
			_, gw := setup(t, func(w http.ResponseWriter, r *http.Request) {
				h := authHeader(config.DefaultAuth(p))
				key := "upstream-test-key"
				if h == "Authorization" {
					key = "Bearer " + key
				}
				if r.Header.Get(h) != key {
					t.Error("wrong upstream auth")
				}
				if r.Header.Get("Cookie") != "" || r.Header.Get("Idempotency-Key") != "" || r.Header.Get("X-Forwarded-For") != "" {
					t.Error("request header leaked")
				}
				if r.URL.RequestURI() != path {
					t.Errorf("path %s", r.URL.RequestURI())
				}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-Balance", "sensitive")
				w.Header().Set("Set-Cookie", "secret")
				w.Header().Set("Trailer", "X-Private")
				io.WriteString(w, `{"usage":{"input_tokens":1,"cache_read_input_tokens":2},"ok":true}`)
				w.Header().Set("X-Private", "sensitive")
			})
			r := request(t, gw.URL, path)
			r.Header.Set("Cookie", "secret")
			r.Header.Set("Idempotency-Key", "secret")
			r.Header.Set("X-Forwarded-For", "secret")
			res, b := call(t, r)
			if res.StatusCode != 200 || !strings.Contains(b, "cache_read_input_tokens") {
				t.Fatal(res.StatusCode, b)
			}
			if res.Header.Get("X-Balance") != "" || res.Header.Get("Set-Cookie") != "" || len(res.Trailer) > 0 {
				t.Fatal("response headers leaked")
			}
		})
	}
}
func TestErrorIsolationAndNoRedirect(t *testing.T) {
	for _, status := range []int{302, 401, 429, 500, 201} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var hits atomic.Int32
			_, gw := setup(t, func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				w.Header().Set("Location", "http://127.0.0.1/private")
				w.Header().Set("Content-Encoding", "gzip")
				w.Header().Set("X-Balance", "SECRET_BALANCE")
				w.WriteHeader(status)
				io.WriteString(w, "SECRET_BALANCE")
			})
			res, b := call(t, request(t, gw.URL, "/v1/messages"))
			want := status
			if status < 400 {
				want = 502
			}
			if res.StatusCode != want || strings.Contains(b, "SECRET_BALANCE") || !json.Valid([]byte(b)) || res.Header.Get("Content-Encoding") != "" || res.Header.Get("Location") != "" {
				t.Fatal(res.StatusCode, b)
			}
			if hits.Load() != 1 {
				t.Fatal("request replayed")
			}
		})
	}
}
func TestRejectBeforeUpstream(t *testing.T) {
	var hits atomic.Int32
	_, gw := setup(t, func(w http.ResponseWriter, r *http.Request) { hits.Add(1) })
	for _, test := range []struct {
		path   string
		modify func(*http.Request)
		code   int
	}{
		{"/v1/chat/completions", func(r *http.Request) { r.Header.Del("Authorization") }, 401},
		{"/v1/chat/completions", func(r *http.Request) { r.Method = "GET" }, 405},
		{"/v1/chat/completions?key=secret", nil, 400},
		{"/v1/chat/completions?alt=sse", nil, 400},
		{"/v1/models", nil, 404},
		{"/v1/chat/completions", func(r *http.Request) { r.URL.Path = "/r/b/v1/chat/completions" }, 404},
		{"/v1/chat/completions", func(r *http.Request) { r.Header.Add("Authorization", r.Header.Get("Authorization")) }, 401},
		{"/v1/chat/completions", func(r *http.Request) { r.Header.Set("Content-Encoding", "gzip") }, 400},
		{"/v1/chat/completions", func(r *http.Request) { r.URL.Path = "/r/a/../v1/chat/completions" }, 400},
	} {
		r := request(t, gw.URL, test.path)
		if test.modify != nil {
			test.modify(r)
		}
		res, _ := call(t, r)
		if res.StatusCode != test.code {
			t.Fatalf("%s got %d want %d", test.path, res.StatusCode, test.code)
		}
	}
	if hits.Load() != 0 {
		t.Fatal("invalid request reached upstream")
	}
}
func TestHotReloadKeepsSSEAndDeletesRoute(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{})
	n, gw := setup(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		close(started)
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		io.WriteString(w, "data: {\"usage\":{\"cache_tokens\":7}}\n\ndata: [DONE]\n\n")
	})
	res, e := http.DefaultClient.Do(request(t, gw.URL, "/v1/chat/completions"))
	if e != nil {
		t.Fatal(e)
	}
	defer res.Body.Close()
	<-started
	reader := bufio.NewReader(res.Body)
	line, e := reader.ReadString('\n')
	if e != nil || line != "data: first\n" {
		t.Fatal(line, e)
	}
	if e = n.Activate(config.Empty(), 2, "v2"); e != nil {
		t.Fatal(e)
	}
	next, _ := call(t, request(t, gw.URL, "/v1/chat/completions"))
	if next.StatusCode != 404 {
		t.Fatal(next.StatusCode)
	}
	close(release)
	tail, e := io.ReadAll(reader)
	if e != nil || !strings.Contains(string(tail), "cache_tokens") || !strings.Contains(string(tail), "[DONE]") {
		t.Fatal(string(tail), e)
	}
}
func TestCancellationReachesUpstream(t *testing.T) {
	canceled := make(chan struct{})
	_, gw := setup(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: start\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(canceled)
	})
	ctx, cancel := context.WithCancel(context.Background())
	r := request(t, gw.URL, "/v1/chat/completions").WithContext(ctx)
	res, e := http.DefaultClient.Do(r)
	if e != nil {
		t.Fatal(e)
	}
	cancel()
	res.Body.Close()
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("cancel not propagated")
	}
}
func TestGzipBytesAndUsage(t *testing.T) {
	_, gw := setup(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "gzip")
		z := gzip.NewWriter(w)
		io.WriteString(z, `{"usage":{"reasoning_tokens":42}}`)
		z.Close()
	})
	r := request(t, gw.URL, "/v1/responses")
	r.Header.Set("Accept-Encoding", "gzip")
	res, e := http.DefaultClient.Do(r)
	if e != nil {
		t.Fatal(e)
	}
	defer res.Body.Close()
	if res.Header.Get("Content-Encoding") != "gzip" {
		t.Fatal("encoding lost")
	}
	z, e := gzip.NewReader(res.Body)
	if e != nil {
		t.Fatal(e)
	}
	b, _ := io.ReadAll(z)
	if string(b) != `{"usage":{"reasoning_tokens":42}}` {
		t.Fatal(string(b))
	}
}
func TestConcurrentUpdates(t *testing.T) {
	n, gw := setup(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{}`)
	})
	old := n.Current()
	cfg := config.Empty()
	cfg.Channels = append(cfg.Channels, old.Channels["a"])
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 50; i++ {
			_ = n.Activate(cfg, int64(i+2), "updated")
		}
	}()
	for i := 0; i < 50; i++ {
		res, _ := call(t, request(t, gw.URL, "/v1/chat/completions"))
		if res.StatusCode != 200 {
			t.Fatal(res.StatusCode)
		}
	}
	<-done
}

func TestAddressCredentialChangeOnlyAffectsNewRequests(t *testing.T) {
	release := make(chan struct{})
	oldFinished := make(chan struct{})
	n, gw := setup(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer upstream-test-key" {
			t.Error("old credential changed")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: old\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		io.WriteString(w, "data: old-complete\n\n")
		close(oldFinished)
	})
	res, e := http.DefaultClient.Do(request(t, gw.URL, "/v1/chat/completions"))
	if e != nil {
		t.Fatal(e)
	}
	defer res.Body.Close()
	fresh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer new-key" {
			t.Error("new credential not applied")
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"source":"new"}`)
	}))
	defer fresh.Close()
	next := testConfig(fresh.URL)
	if e = n.Activate(next, 2, "second"); e != nil {
		t.Fatal(e)
	}
	newRequest := request(t, gw.URL, "/v1/chat/completions")
	newRequest.Header.Set("Authorization", "Bearer new-key")
	response, body := call(t, newRequest)
	if response.StatusCode != 200 || !strings.Contains(body, "new") {
		t.Fatal(body)
	}
	close(release)
	tail, e := io.ReadAll(res.Body)
	if e != nil || !strings.Contains(string(tail), "old-complete") {
		t.Fatal(string(tail), e)
	}
	<-oldFinished
}
func TestUpstreamTimeoutAndStreamFailure(t *testing.T) {
	t.Run("headers", func(t *testing.T) {
		n, gw := setup(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.Copy(io.Discard, r.Body); <-r.Context().Done() })
		n.proxy.transport.ResponseHeaderTimeout = 30 * time.Millisecond
		res, _ := call(t, request(t, gw.URL, "/v1/responses"))
		if res.StatusCode != 504 {
			t.Fatal(res.StatusCode)
		}
	})
	t.Run("stream", func(t *testing.T) {
		n, gw := setup(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "data: first\n\n")
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		})
		n.proxy.idle = 30 * time.Millisecond
		res, e := http.DefaultClient.Do(request(t, gw.URL, "/v1/responses"))
		if e != nil {
			t.Fatal(e)
		}
		defer res.Body.Close()
		b, e := io.ReadAll(res.Body)
		if e == nil {
			t.Fatal("broken stream appeared complete")
		}
		if strings.Contains(string(b), "[DONE]") || strings.Contains(string(b), "gateway_") {
			t.Fatal("fabricated stream completion")
		}
	})
}
func TestEmbeddedSuccessErrorIsExplicitKnownGap(t *testing.T) {
	body := `{"error":{"message":"SYNTHETIC_EMBEDDED_ERROR"}}`
	_, gw := setup(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, body)
	})
	res, got := call(t, request(t, gw.URL, "/v1/chat/completions"))
	if res.StatusCode != 200 || got != body {
		t.Fatal("standard passthrough semantics changed")
	}
}

func TestCredentialCarriersRejectAmbiguity(t *testing.T) {
	var hits atomic.Int32
	_, gw := setup(t, func(w http.ResponseWriter, r *http.Request) { hits.Add(1) })
	for _, test := range []struct {
		name, path string
		change     func(*http.Request)
	}{
		{"missing bearer", "/v1/chat/completions", func(r *http.Request) { r.Header.Del("Authorization") }},
		{"empty bearer", "/v1/chat/completions", func(r *http.Request) { r.Header.Set("Authorization", "Bearer ") }},
		{"basic", "/v1/chat/completions", func(r *http.Request) { r.Header.Set("Authorization", "Basic fake") }},
		{"space in key", "/v1/chat/completions", func(r *http.Request) { r.Header.Set("Authorization", "Bearer two keys") }},
		{"joined keys", "/v1/chat/completions", func(r *http.Request) { r.Header.Set("Authorization", "Bearer one,two") }},
		{"multiple carriers", "/v1/messages", func(r *http.Request) { r.Header.Set("Authorization", "Bearer alternate") }},
		{"duplicate api key", "/v1/messages", func(r *http.Request) { r.Header.Add("X-Api-Key", "alternate") }},
		{"missing gemini key", "/v1beta/models/test:generateContent", func(r *http.Request) { r.Header.Del("X-Goog-Api-Key") }},
		{"query key", "/v1beta/models/test:generateContent?key=forbidden", func(r *http.Request) {}},
		{"hop auth", "/v1/chat/completions", func(r *http.Request) { r.Header.Set("Connection", "Authorization") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := request(t, gw.URL, test.path)
			test.change(r)
			res, _ := call(t, r)
			if res.StatusCode != 400 && res.StatusCode != 401 {
				t.Fatal("invalid carrier accepted", res.StatusCode)
			}
		})
	}
	if hits.Load() != 0 {
		t.Fatal("invalid credentials reached upstream")
	}
}
func TestRequestKeysAreIndependentWithoutConfigChanges(t *testing.T) {
	n, gw := setup(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Key string `json:"key"`
		}
		if e := json.NewDecoder(r.Body).Decode(&body); e != nil {
			t.Error(e)
		}
		if r.Header.Get("Authorization") != "Bearer "+body.Key {
			t.Error("request credentials mixed across callers")
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"ok":true}`)
	})
	// This test exercises credential isolation, not overload. Permit all 20 callers.
	c := testConfig(n.Current().Channels["a"].BaseURL)
	c.TrafficLimits = config.TrafficLimits{Node: 32, Upstream: 32}
	if err := n.Activate(c, 1, "test"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := fmt.Sprintf("fake-caller-key-%d", i)
			r := request(t, gw.URL, "/v1/chat/completions")
			body := `{"key":"` + key + `"}`
			r.Body = io.NopCloser(strings.NewReader(body))
			r.ContentLength = int64(len(body))
			r.GetBody = nil
			r.Header.Set("Authorization", "Bearer "+key)
			res, _ := call(t, r)
			if res.StatusCode != 200 {
				t.Error(res.StatusCode)
			}
		}(i)
	}
	wg.Wait()
	if n.Current().Version != 1 {
		t.Fatal("rotation unexpectedly required config publication")
	}
}
func TestCredentialFormatOverridePreservesCallerKey(t *testing.T) {
	n, gw := setup(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer upstream-test-key" || r.Header.Get("X-Api-Key") != "" {
			t.Error("override changed key or forwarded extra carrier")
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{}`)
	})
	old := testConfig(n.Current().Channels["a"].BaseURL).Channels[0]
	old.Protocols["anthropic_messages"] = config.Endpoint{Auth: "bearer"}
	if e := n.Activate(config.Config{Schema: config.Schema, Channels: []config.Channel{old}}, 2, "override"); e != nil {
		t.Fatal(e)
	}
	res, _ := call(t, request(t, gw.URL, "/v1/messages"))
	if res.StatusCode != 200 {
		t.Fatal(res.StatusCode)
	}
}
func TestUpstreamAuthorizesKeyAndErrorsDoNotLeakIt(t *testing.T) {
	const secret = "FAKE_SECRET_REJECTED_BY_UPSTREAM"
	var hits atomic.Int32
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(previous)
	_, gw := setup(t, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Header.Get("Authorization") != "Bearer "+secret {
			t.Error("caller key was changed")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(401)
		io.WriteString(w, `{"error":"invalid key `+secret+`"}`)
	})
	r := request(t, gw.URL, "/v1/chat/completions")
	r.Header.Set("Authorization", "Bearer "+secret)
	res, body := call(t, r)
	if res.StatusCode != 401 || hits.Load() != 1 {
		t.Fatal("upstream did not authorize the request")
	}
	if strings.Contains(body, secret) || strings.Contains(logs.String(), secret) {
		t.Fatal("request key leaked in error or log")
	}
}
