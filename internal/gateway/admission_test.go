package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/halfisnine/upstream-gateway/internal/config"
)

func TestAdmissionSharesOriginsAndCountsAcrossVersions(t *testing.T) {
	n := New(nil, nil, "limits")
	defer n.Close()
	c := testConfig("https://example.invalid")
	c.TrafficLimits = config.TrafficLimits{Node: 3, Upstream: 2}
	if err := n.Activate(c, 1, "one"); err != nil {
		t.Fatal(err)
	}
	a, _ := url.Parse("https://EXAMPLE.invalid/a")
	b, _ := url.Parse("https://example.invalid:443/b")
	other, _ := url.Parse("https://other.invalid")
	r1, ok := n.admit(a)
	if !ok {
		t.Fatal("first request")
	}
	r2, ok := n.admit(b)
	if !ok {
		t.Fatal("second request")
	}
	if _, ok := n.admit(a); ok {
		t.Fatal("origin limit bypassed")
	}
	r3, ok := n.admit(other)
	if !ok {
		t.Fatal("other host shares wrong budget")
	}
	if _, ok := n.admit(other); ok {
		t.Fatal("node limit bypassed")
	}
	c.TrafficLimits = config.TrafficLimits{Node: 1, Upstream: 1}
	if err := n.Activate(c, 2, "two"); err != nil {
		t.Fatal(err)
	}
	r1()
	r2()
	if _, ok := n.admit(a); ok {
		t.Fatal("old active request excluded after lower")
	}
	r3()
	r4, ok := n.admit(a)
	if !ok {
		t.Fatal("capacity not released")
	}
	r4()
	c.TrafficLimits.Node = -1
	if n.Activate(c, 3, "invalid") == nil || n.Current().Version != 2 {
		t.Fatal("invalid update replaced good snapshot")
	}
	if len(n.upstreamActive) != 0 || n.active.Load() != 0 {
		t.Fatal("counter leaked")
	}
	n.Drain()
	if _, ok := n.admit(a); ok {
		t.Fatal("draining node admitted")
	}
}

func TestAdmissionConcurrentRequests(t *testing.T) {
	n := New(nil, nil, "parallel")
	defer n.Close()
	c := testConfig("http://example.invalid")
	c.TrafficLimits = config.TrafficLimits{Node: 13, Upstream: 7}
	if err := n.Activate(c, 1, "one"); err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(c.Channels[0].BaseURL)
	var wg sync.WaitGroup
	for range 200 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if release, ok := n.admit(u); ok {
				if n.active.Load() > 7 {
					t.Error("concurrent admission exceeded budget")
				}
				release()
			}
		}()
	}
	wg.Wait()
	if n.active.Load() != 0 || len(n.upstreamActive) != 0 {
		t.Fatal("capacity leaked")
	}
}

func TestHotLimitsPreserveStreamsHTTP1AndHTTP2(t *testing.T) {
	for _, h2 := range []bool{false, true} {
		name := "http1"
		if h2 {
			name = "http2"
		}
		t.Run(name, func(t *testing.T) {
			finish := make(chan struct{})
			up := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				if (r.ProtoMajor == 2) != h2 {
					t.Errorf("unexpected upstream protocol %s", r.Proto)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, "data: start\n\n")
				w.(http.Flusher).Flush()
				select {
				case <-finish:
					io.WriteString(w, "data: complete\n\n")
				case <-r.Context().Done():
				}
			}))
			up.EnableHTTP2 = h2
			if h2 {
				up.StartTLS()
			} else {
				up.Start()
			}
			defer up.Close()
			n := New(nil, nil, "streams")
			defer n.Close()
			if h2 {
				n.proxy.transport.TLSClientConfig = up.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
			}
			c := testConfig(up.URL)
			c.TrafficLimits = config.TrafficLimits{Node: 3, Upstream: 3}
			if err := n.Activate(c, 1, "one"); err != nil {
				t.Fatal(err)
			}
			gw := httptest.NewServer(n.Handler())
			defer gw.Close()
			client := &http.Client{Timeout: 10 * time.Second}
			var bodies []io.ReadCloser
			defer func() {
				close(finish)
				for _, b := range bodies {
					b.Close()
				}
			}()
			start := func(want int) {
				t.Helper()
				res, err := client.Do(request(t, gw.URL, "/v1/chat/completions"))
				if err != nil {
					t.Fatal(err)
				}
				if res.StatusCode != want {
					res.Body.Close()
					t.Fatalf("status %d, want %d", res.StatusCode, want)
				}
				if want == 200 {
					bodies = append(bodies, res.Body)
				} else {
					defer res.Body.Close()
					if res.Header.Get("Retry-After") != "1" {
						t.Fatal("missing overload hint")
					}
				}
			}
			for range 3 {
				start(200)
			}
			start(503)
			c.TrafficLimits = config.TrafficLimits{Node: 40, Upstream: 40}
			if err := n.Activate(c, 2, "two"); err != nil {
				t.Fatal(err)
			}
			for range 37 {
				start(200)
			}
			start(503)
			c.TrafficLimits = config.TrafficLimits{Node: 1, Upstream: 1}
			if err := n.Activate(c, 3, "three"); err != nil {
				t.Fatal(err)
			}
			start(503)
			// Completing old streams remains possible after a lower limit. Signal
			// each handler without closing twice in cleanup.
			for range 40 {
				finish <- struct{}{}
			}
			for _, b := range bodies {
				raw, err := io.ReadAll(b)
				b.Close()
				if err != nil || !strings.Contains(string(raw), "data: complete") {
					t.Fatal("old stream interrupted", err)
				}
			}
			deadline := time.Now().Add(time.Second)
			for n.active.Load() != 0 && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if n.active.Load() != 0 {
				t.Fatal("completed streams leaked capacity")
			}
			start(200)
		})
	}
}
