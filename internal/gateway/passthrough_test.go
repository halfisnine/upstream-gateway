package gateway

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/halfisnine/upstream-gateway/internal/config"
	"github.com/halfisnine/upstream-gateway/internal/errorrules"
	"github.com/halfisnine/upstream-gateway/internal/interceptlog"
)

func allowedRequest(t *testing.T, gw, method, path string) *http.Request {
	t.Helper()
	r, err := http.NewRequest(method, gw+"/r/a"+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Authorization", "Bearer client-key")
	return r
}

func TestAllowedPathPreservesResponsesAndSkipsInterception(t *testing.T) {
	for _, status := range []int{200, 201, 204, 302, 304, 401, 403, 429, 500} {
		for _, zipped := range []bool{false, true} {
			name := http.StatusText(status)
			if zipped {
				name += " gzip"
			}
			t.Run(name, func(t *testing.T) {
				raw := []byte(`{"error":{"code":"insufficient_quota","message":"original balance message"}}` + strings.Repeat(" ", 70<<10))
				var encoded bytes.Buffer
				if zipped {
					z := gzip.NewWriter(&encoded)
					z.Write(raw)
					z.Close()
				} else {
					encoded.Write(raw)
				}
				var hits atomic.Int32
				n, gw := setup(t, func(w http.ResponseWriter, r *http.Request) {
					hits.Add(1)
					if r.URL.Path != "/prefix/v1/models" || r.URL.RawQuery != "limit=10&tag=a&tag=b" || r.Header.Get("Authorization") != "Bearer client-key" {
						t.Error("request routing or credential changed")
					}
					w.Header().Set("Content-Type", "text/plain; charset=utf-8")
					w.Header().Set("X-Upstream-Detail", "preserved")
					w.Header().Set("Retry-After", "600")
					w.Header().Set("Location", "/must-not-follow")
					w.Header().Set("Connection", "X-Hop-Response")
					w.Header().Set("X-Hop-Response", "drop")
					if zipped {
						w.Header().Set("Content-Encoding", "gzip")
					}
					w.WriteHeader(status)
					if status != 204 && status != 304 {
						w.Write(encoded.Bytes())
					}
				})
				c := testConfig(n.Current().Channels["a"].BaseURL + "/prefix")
				c.InspectEmbeddedErrors = true
				c.ErrorRules = errorrules.Presets()
				c.Channels[0].PathAllowlist = []config.AllowedPath{{Method: "GET", Path: "/v1/models", Protocol: "openai_chat"}}
				if err := n.Activate(c, 2, "allow"); err != nil {
					t.Fatal(err)
				}
				sink := &eventSink{}
				n.Logs = interceptlog.New(sink)
				client := &http.Client{Transport: &http.Transport{DisableCompression: true}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
				defer client.CloseIdleConnections()
				resp, err := client.Do(allowedRequest(t, gw.URL, "GET", "/v1/models?limit=10&tag=a&tag=b"))
				if err != nil {
					t.Fatal(err)
				}
				body, err := io.ReadAll(resp.Body)
				resp.Body.Close()
				if err != nil || resp.StatusCode != status || hits.Load() != 1 {
					t.Fatal("response changed", resp.StatusCode, err, hits.Load())
				}
				if status != 204 && status != 304 && !bytes.Equal(body, encoded.Bytes()) {
					t.Fatal("response body changed")
				}
				if resp.Header.Get("X-Upstream-Detail") != "preserved" || resp.Header.Get("Retry-After") != "600" || resp.Header.Get("Location") != "/must-not-follow" || resp.Header.Get("X-Hop-Response") != "" {
					t.Fatal("response headers changed", resp.Header)
				}
				n.Logs.Close()
				if len(sink.events) != 0 {
					t.Fatal("passthrough created interception log")
				}
			})
		}
	}
}

func TestAllowlistUsesSelectedProtocolAndExactMatch(t *testing.T) {
	var hits atomic.Int32
	override := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		body, _ := io.ReadAll(r.Body)
		if r.Method != "PATCH" || r.URL.Path != "/custom/catalog" || string(body) != "raw form data" || r.Header.Get("Authorization") != "Bearer client-key" || r.Header.Get("X-Api-Key") != "" || r.Header.Get("Anthropic-Version") != "2023-06-01" {
			t.Error("protocol override or body lost")
		}
		if r.Header.Get("Cookie") != "" || r.Header.Get("X-Forwarded-Host") != "" || r.Header.Get("X-Hop") != "" {
			t.Error("untrusted headers forwarded")
		}
		w.WriteHeader(202)
		io.WriteString(w, "accepted")
	}))
	defer override.Close()
	n, gw := setup(t, func(w http.ResponseWriter, r *http.Request) { t.Error("wrong upstream") })
	c := testConfig(n.Current().Channels["a"].BaseURL)
	c.Channels[0].Protocols["anthropic_messages"] = config.Endpoint{BaseURL: override.URL + "/custom", Auth: "bearer"}
	c.Channels[0].PathAllowlist = []config.AllowedPath{{Method: "PATCH", Path: "/catalog", Protocol: "anthropic_messages"}}
	if err := n.Activate(c, 2, "allow"); err != nil {
		t.Fatal(err)
	}
	r, _ := http.NewRequest("PATCH", gw.URL+"/r/a/catalog", strings.NewReader("raw form data"))
	r.Header.Set("X-Api-Key", "client-key")
	r.Header.Set("Anthropic-Version", "2023-06-01")
	r.Header.Set("Content-Type", "text/plain")
	r.Header.Set("Cookie", "private")
	r.Header.Set("X-Forwarded-Host", "elsewhere")
	r.Header.Set("Connection", "X-Hop")
	r.Header.Set("X-Hop", "drop")
	resp, body := call(t, r)
	if resp.StatusCode != 202 || body != "accepted" {
		t.Fatal(resp.StatusCode, body)
	}
	for _, path := range []string{"/catalog/extra", "/catalog/", "/Catalog", "/catalog%2fextra", "/../catalog", "//catalog", "/catalog?bad=%zz"} {
		r := allowedRequest(t, gw.URL, "PATCH", path)
		resp, _ := call(t, r)
		if resp.StatusCode != 400 && resp.StatusCode != 404 {
			t.Fatal(path, resp.StatusCode)
		}
	}
	resp, _ = call(t, allowedRequest(t, gw.URL, "GET", "/catalog"))
	if resp.StatusCode != 404 {
		t.Fatal("method widened", resp.StatusCode)
	}
	if hits.Load() != 1 {
		t.Fatal("unmatched request reached upstream")
	}
	// Incoming protocol authentication is still required even on an allowed path.
	r = allowedRequest(t, gw.URL, "PATCH", "/catalog")
	resp, _ = call(t, r)
	if resp.StatusCode != 401 {
		t.Fatal("wrong auth accepted")
	}
}

func TestAllowedPathReloadKeepsStreamAndSharesAdmission(t *testing.T) {
	finish := make(chan struct{})
	defer close(finish)
	n, gw := setup(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(403)
		io.WriteString(w, "data: original error\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-finish:
		case <-r.Context().Done():
			return
		}
		io.WriteString(w, "data: final\n\n")
	})
	c := testConfig(n.Current().Channels["a"].BaseURL)
	c.TrafficLimits = config.TrafficLimits{Node: 1, Upstream: 1}
	c.Channels[0].PathAllowlist = []config.AllowedPath{{Method: "GET", Path: "/v1/models", Protocol: "openai_chat"}}
	if err := n.Activate(c, 2, "allow"); err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(allowedRequest(t, gw.URL, "GET", "/v1/models"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	first := make([]byte, len("data: original error\n\n"))
	if _, err = io.ReadFull(resp.Body, first); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 403 {
		t.Fatal("stream error rewritten")
	}
	blocked, _ := call(t, request(t, gw.URL, "/v1/chat/completions"))
	if blocked.StatusCode != 503 {
		t.Fatal("allowlist did not share capacity")
	}
	// Caller mutation cannot alter the already activated snapshot.
	c.Channels[0].PathAllowlist[0].Path = "/different"
	if n.Current().Channels["a"].PathAllowlist[0].Path != "/v1/models" {
		t.Fatal("snapshot is mutable")
	}
	c.Channels[0].PathAllowlist = nil
	if err = n.Activate(c, 3, "removed"); err != nil {
		t.Fatal(err)
	}
	blocked, _ = call(t, allowedRequest(t, gw.URL, "GET", "/v1/models"))
	if blocked.StatusCode != 404 {
		t.Fatal("removed path accepted")
	}
	// Removal only affects new requests; the original stream can finish normally.
	finish <- struct{}{}
	rest, err := io.ReadAll(resp.Body)
	if err != nil || string(rest) != "data: final\n\n" {
		t.Fatal("reload interrupted stream", string(rest), err)
	}
	resp.Body.Close()
	deadline := time.Now().Add(time.Second)
	for n.active.Load() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if n.active.Load() != 0 {
		t.Fatal("completion leaked slot")
	}
	u, _ := url.Parse(c.Channels[0].BaseURL)
	release, ok := n.admit(u)
	if !ok {
		t.Fatal("slot not reusable")
	}
	release()
}

func TestAllowedHeadTrailersAndKnownPathOverride(t *testing.T) {
	n, gw := setup(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "HEAD" {
			w.Header().Set("Content-Length", "123")
			w.WriteHeader(200)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Trailer", "X-Catalog-End")
		w.WriteHeader(402)
		io.WriteString(w, `{"error":{"message":"original"}}`)
		w.Header().Set("X-Catalog-End", "complete")
	})
	c := testConfig(n.Current().Channels["a"].BaseURL)
	c.Channels[0].PathAllowlist = []config.AllowedPath{{Method: "HEAD", Path: "/v1/models", Protocol: "openai_chat"}, {Method: "POST", Path: "/v1/chat/completions", Protocol: "openai_chat"}}
	c.InspectEmbeddedErrors = true
	c.ErrorRules = errorrules.Presets()
	if err := n.Activate(c, 2, "allow"); err != nil {
		t.Fatal(err)
	}
	resp, body := call(t, allowedRequest(t, gw.URL, "HEAD", "/v1/models"))
	if resp.StatusCode != 200 || body != "" || resp.ContentLength != 123 {
		t.Fatal("HEAD changed", resp.StatusCode, resp.ContentLength, body)
	}
	resp, body = call(t, request(t, gw.URL, "/v1/chat/completions"))
	if resp.StatusCode != 402 || body != `{"error":{"message":"original"}}` || resp.Trailer.Get("X-Catalog-End") != "complete" {
		t.Fatal("explicit override or trailer failed", resp.StatusCode, body, resp.Trailer)
	}
	// Removing the exception restores ordinary error masking on the same path.
	c.Channels[0].PathAllowlist = nil
	if err := n.Activate(c, 3, "removed"); err != nil {
		t.Fatal(err)
	}
	resp, body = call(t, request(t, gw.URL, "/v1/chat/completions"))
	if strings.Contains(body, "original") {
		t.Fatal("built-in protection was not restored")
	}
}

func TestAllowedGetDoesNotRetryAfterUpstreamDisconnect(t *testing.T) {
	var hits atomic.Int32
	n, gw := setup(t, func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 2 {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			conn.Close()
			return
		}
		io.WriteString(w, "ok")
	})
	c := testConfig(n.Current().Channels["a"].BaseURL)
	c.Channels[0].PathAllowlist = []config.AllowedPath{{Method: "GET", Path: "/v1/models", Protocol: "openai_chat"}}
	if err := n.Activate(c, 2, "allow"); err != nil {
		t.Fatal(err)
	}
	call(t, allowedRequest(t, gw.URL, "GET", "/v1/models"))
	resp, _ := call(t, allowedRequest(t, gw.URL, "GET", "/v1/models"))
	if resp.StatusCode != 502 || hits.Load() != 2 {
		t.Fatal("unexpected retry", resp.StatusCode, hits.Load())
	}
}

func TestAllowedStreamCancellationReleasesUpstream(t *testing.T) {
	canceled := make(chan struct{})
	var hits atomic.Int32
	n, gw := setup(t, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		io.WriteString(w, "start")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(canceled)
	})
	c := testConfig(n.Current().Channels["a"].BaseURL)
	c.Channels[0].PathAllowlist = []config.AllowedPath{{Method: "GET", Path: "/v1/models", Protocol: "openai_chat"}}
	if err := n.Activate(c, 2, "allow"); err != nil {
		t.Fatal(err)
	}
	r := allowedRequest(t, gw.URL, "GET", "/v1/models")
	r.ContentLength = 17 << 20
	rec := httptest.NewRecorder()
	n.Handler().ServeHTTP(rec, r)
	if rec.Code != 413 || hits.Load() != 0 {
		t.Fatal("body limit bypassed")
	}
	resp, err := http.DefaultClient.Do(allowedRequest(t, gw.URL, "GET", "/v1/models"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("upstream cancellation not propagated")
	}
}
