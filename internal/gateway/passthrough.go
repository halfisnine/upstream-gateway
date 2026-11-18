package gateway

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Hop-by-hop fields belong to each connection, not to the upstream response.
func passthroughBlocked(h http.Header) map[string]bool {
	blocked := connectionHeaders(h)
	for _, name := range []string{"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade"} {
		blocked[name] = true
	}
	return blocked
}

func copyPassthroughRequestHeaders(dst, src http.Header) {
	blocked := passthroughBlocked(src)
	// Credentials are forwarded separately using the selected protocol. Keep
	// browser sessions and untrusted proxy routing information out of upstreams.
	for _, name := range []string{"Authorization", "X-Api-Key", "X-Goog-Api-Key", "Cookie", "Origin", "Referer", "Forwarded", "Host", "Content-Length"} {
		blocked[name] = true
	}
	for name, values := range src {
		if blocked[http.CanonicalHeaderKey(name)] || strings.HasPrefix(strings.ToLower(name), "x-forwarded-") {
			continue
		}
		dst[name] = append([]string(nil), values...)
	}
}

func (p *Proxy) passthroughResponse(w *observedWriter, r *http.Request, resp *http.Response, cancel context.CancelFunc) {
	controller := http.NewResponseController(w)
	defer controller.SetWriteDeadline(time.Time{})
	_ = controller.SetWriteDeadline(time.Now().Add(p.write))
	// HTTP upgrades require a different bidirectional transport and cannot be
	// represented by this finite HTTP forwarding path.
	if resp.StatusCode == http.StatusSwitchingProtocols {
		safeError(w, "", http.StatusBadGateway, "gw_"+RandomID())
		return
	}
	blocked := passthroughBlocked(resp.Header)
	for name, values := range resp.Header {
		if !blocked[http.CanonicalHeaderKey(name)] {
			w.Header()[name] = append([]string(nil), values...)
		}
	}
	var trailers []string
	for name := range resp.Trailer {
		if !blocked[http.CanonicalHeaderKey(name)] && name != "Content-Length" {
			trailers = append(trailers, name)
		}
	}
	sort.Strings(trailers)
	if len(trailers) > 0 {
		w.Header().Del("Content-Length")
		w.Header().Set("Trailer", strings.Join(trailers, ", "))
	}
	w.WriteHeader(resp.StatusCode)
	if r.Method == http.MethodHead || resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusNotModified {
		return
	}
	// This path deliberately never decodes bodies, evaluates error rules or
	// creates interception logs. Streaming keeps memory bounded even for errors.
	p.copyResponse(w, resp.Body, true, cancel)
	for _, name := range trailers {
		w.Header()[name] = append([]string(nil), resp.Trailer.Values(name)...)
	}
}
