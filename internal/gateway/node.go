package gateway

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/halfisnine/upstream-gateway/internal/interceptlog"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/halfisnine/upstream-gateway/internal/config"
	"github.com/halfisnine/upstream-gateway/internal/store"
)

type Snapshot struct {
	Version  int64
	Digest   string
	Channels map[string]config.Channel
	Rules    []config.Rule
	Inspect  bool
}
type Node struct {
	Store        *store.Store
	Logs         *interceptlog.Writer
	Key          []byte
	ID, Instance string
	current      atomic.Pointer[Snapshot]
	draining     atomic.Bool
	active       atomic.Int64
	syncMu       sync.Mutex
	statusMu     sync.Mutex
	status       store.Node
	slots        chan struct{}
	proxy        *Proxy
}

func New(s *store.Store, key []byte, id string) *Node {
	n := &Node{Store: s, Key: key, ID: id, Instance: RandomID(), slots: make(chan struct{}, 32)}
	if s != nil {
		n.Logs = interceptlog.New(s)
	}
	n.proxy = NewProxy(n)
	return n
}
func RandomID() string {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		panic("random_source_failed")
	}
	return hex.EncodeToString(b)
}
func (n *Node) Current() *Snapshot { return n.current.Load() }
func (n *Node) Ready() bool        { return !n.draining.Load() && n.Current() != nil }
func (n *Node) Drain()             { n.draining.Store(true) }
func (n *Node) Close() {
	n.Drain()
	n.proxy.transport.CloseIdleConnections()
	n.Logs.Close()
	if n.Store != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = n.Store.Release(ctx, n.ID, n.Instance)
	}
}
func (n *Node) Status() store.Node {
	n.statusMu.Lock()
	defer n.statusMu.Unlock()
	s := n.status
	s.Inflight = n.active.Load()
	s.Logs = n.Logs.Stats()
	s.Logs.Instance = n.Instance
	return s
}
func (n *Node) Activate(c config.Config, v int64, digest string) error {
	if err := config.Validate(c); err != nil {
		return err
	}
	raw, _ := json.Marshal(c.ErrorRules)
	var rules []config.Rule
	_ = json.Unmarshal(raw, &rules)
	snap := &Snapshot{Version: v, Digest: digest, Channels: map[string]config.Channel{}, Rules: rules, Inspect: c.InspectEmbeddedErrors}
	for _, ch := range c.Channels { // Take ownership without retaining the caller's mutable maps.
		ps := make(map[string]config.Endpoint, len(ch.Protocols))
		for p, e := range ch.Protocols {
			ps[p] = e
		}
		ch.Protocols = ps
		snap.Channels[ch.ID] = ch
	}
	n.current.Store(snap)
	return nil
}
func (n *Node) Sync(ctx context.Context) error {
	n.syncMu.Lock()
	defer n.syncMu.Unlock()
	status := n.Status()
	status.ID = n.ID
	status.Instance = n.Instance
	if s := n.Current(); s != nil {
		status.Applied = s.Version
		status.Digest = s.Digest
	}
	// Claim the node identity before loading or acknowledging anything.
	if err := n.Store.Report(ctx, status); err != nil {
		if errors.Is(err, store.ErrNodeConflict) {
			n.Drain()
		}
		status.Error = "database_unavailable"
		if errors.Is(err, store.ErrNodeConflict) {
			status.Error = "node_id_in_use"
		}
		n.setStatus(status)
		return err
	}
	desired, err := n.Store.Latest(ctx)
	if err != nil {
		status.Error = "database_unavailable"
	} else {
		status.Desired = desired
		status.Error = ""
		if desired > status.Applied {
			v, e := n.Store.Version(ctx, desired)
			if e != nil {
				status.Error = "snapshot_read_failed"
				err = e
			} else {
				c, e := config.Open(v.Data, v.Digest, n.Key)
				if e == nil {
					e = n.Activate(c, v.ID, v.Digest)
				}
				if e != nil {
					status.Error = "snapshot_validation_failed"
					if errors.Is(e, config.ErrLegacyConfig) {
						status.Error = "legacy_configuration_requires_republish"
					}
					err = e
				} else {
					status.Applied = v.ID
					status.Digest = v.Digest
				}
			}
		}
	}
	status.Seen = time.Now().UTC()
	status.Inflight = n.active.Load()
	status.Logs = n.Logs.Stats()
	status.Logs.Instance = n.Instance
	n.setStatus(status)
	reportErr := n.Store.Report(ctx, status)
	if errors.Is(reportErr, store.ErrNodeConflict) {
		n.Drain()
	}
	if err != nil {
		return err
	}
	return reportErr
}
func (n *Node) setStatus(s store.Node) { n.statusMu.Lock(); n.status = s; n.statusMu.Unlock() }
func (n *Node) Run(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		c, cancel := context.WithTimeout(ctx, 4*time.Second)
		err := n.Sync(c)
		cancel()
		if err != nil {
			slog.Warn("configuration sync failed", "node", n.ID, "category", n.Status().Error)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
