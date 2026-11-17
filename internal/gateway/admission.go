package gateway

import (
	"net"
	"net/url"
	"strings"
)

// originKey shares capacity across channels, paths and credentials using the
// same scheme/host/port, including an explicit default port and IPv6 literals.
func originKey(u *url.URL) string {
	port := u.Port()
	if port == "" {
		port = "443"
		if u.Scheme == "http" {
			port = "80"
		}
	}
	return u.Scheme + "://" + net.JoinHostPort(strings.ToLower(u.Hostname()), port)
}

// Counts survive configuration changes. Lowering a limit drains naturally;
// increasing it admits new requests without replacing a live transport or SSE.
// No unbounded waiting queue is maintained. Admission uses the latest limits,
// while the accepted request retains its original routing/rule snapshot.
func (n *Node) admit(target *url.URL) (func(), bool) {
	key := originKey(target)
	n.admissionMu.Lock()
	defer n.admissionMu.Unlock()
	snap := n.Current()
	if snap == nil || n.draining.Load() || n.active.Load() >= int64(snap.Limits.Node) || n.upstreamActive[key] >= snap.Limits.Upstream {
		return nil, false
	}
	n.active.Add(1)
	n.upstreamActive[key]++
	return func() {
		n.admissionMu.Lock()
		defer n.admissionMu.Unlock()
		n.active.Add(-1)
		n.upstreamActive[key]--
		if n.upstreamActive[key] == 0 {
			delete(n.upstreamActive, key)
		}
	}, true
}
