// Package interceptlog provides bounded, best-effort metadata logging.
package interceptlog

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"
)

const QueueSize = 1024
const BatchSize = 50
const MaxRecordBytes = 4096
const QueueBytes = 1 << 20

type Event struct {
	OriginalCipher    []byte `json:"original_cipher,omitempty"`
	OriginalState     string `json:"original_state,omitempty"`
	OriginalTruncated bool   `json:"original_truncated,omitempty"`
	OriginalRedacted  bool   `json:"original_redacted,omitempty"`
	queueBytes        int64
	ID                string    `json:"id"`
	Time              time.Time `json:"time"`
	RequestID         string    `json:"request_id"`
	Node              string    `json:"node"`
	Instance          string    `json:"instance"`
	Channel           string    `json:"channel"`
	Protocol          string    `json:"protocol"`
	Version           int64     `json:"version"`
	RuleID            string    `json:"rule_id"`
	Group             int       `json:"group"`
	Reason            string    `json:"reason"`
	Source            string    `json:"source"`
	Upstream          int       `json:"upstream_status"`
	Desired           int       `json:"desired_status"`
	Actual            int       `json:"actual_status"`
	Action            string    `json:"action"`
}
type Stats struct {
	Instance     string `json:"instance"`
	Pending      int64  `json:"pending"`
	PendingBytes int64  `json:"pending_bytes"`
	Dropped      int64  `json:"dropped"`
	Failures     int64  `json:"write_failures"`
	LastSuccess  int64  `json:"last_success_unix"`
	LastFailure  int64  `json:"last_failure_unix"`
}
type Sink interface {
	WriteEvents(context.Context, []Event) error
	PruneEvents(context.Context) error
}
type Writer struct {
	sink                                                 Sink
	queue                                                chan Event
	pending, dropped, failures, lastSuccess, lastFailure atomic.Int64
	pendingBytes                                         atomic.Int64
	stop, done                                           chan struct{}
	once                                                 sync.Once
	mu                                                   sync.RWMutex
	closed                                               bool
}

func New(s Sink) *Writer {
	w := &Writer{sink: s, queue: make(chan Event, QueueSize), stop: make(chan struct{}), done: make(chan struct{})}
	go w.run()
	return w
}
func (w *Writer) Enqueue(e Event) {
	if w == nil {
		return
	}
	raw, err := json.Marshal(e)
	if err != nil || len(raw) > MaxRecordBytes {
		w.dropped.Add(1)
		return
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	if w.closed {
		w.dropped.Add(1)
		return
	}
	// pending includes the retry batch, so retries cannot grow the memory budget.
	if w.pending.Add(1) > QueueSize {
		w.pending.Add(-1)
		w.dropped.Add(1)
		return
	}
	e.queueBytes = int64(len(raw))
	if w.pendingBytes.Add(e.queueBytes) > QueueBytes {
		w.pendingBytes.Add(-e.queueBytes)
		w.pending.Add(-1)
		w.dropped.Add(1)
		return
	}
	select {
	case w.queue <- e:
	default:
		w.pendingBytes.Add(-e.queueBytes)
		w.pending.Add(-1)
		w.dropped.Add(1)
	}
}
func (w *Writer) Stats() Stats {
	if w == nil {
		return Stats{}
	}
	return Stats{Pending: w.pending.Load(), PendingBytes: w.pendingBytes.Load(), Dropped: w.dropped.Load(), Failures: w.failures.Load(), LastSuccess: w.lastSuccess.Load(), LastFailure: w.lastFailure.Load()}
}
func (w *Writer) Close() {
	if w == nil {
		return
	}
	w.once.Do(func() { w.mu.Lock(); w.closed = true; w.mu.Unlock(); close(w.stop) })
	<-w.done
}
func (w *Writer) run() {
	defer close(w.done)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	batch := make([]Event, 0, BatchSize)
	backoff := time.Second
	var next time.Time
	lastPrune := time.Now()
	fill := func() {
		for len(batch) < BatchSize {
			select {
			case e := <-w.queue:
				batch = append(batch, e)
			default:
				return
			}
		}
	}
	write := func(ctx context.Context) bool {
		if len(batch) == 0 {
			return true
		}
		if w.sink.WriteEvents(ctx, batch) != nil {
			w.failures.Add(1)
			w.lastFailure.Store(time.Now().Unix())
			return false
		}
		for _, event := range batch {
			w.pendingBytes.Add(-event.queueBytes)
		}
		w.pending.Add(-int64(len(batch)))
		clear(batch)
		batch = batch[:0]
		w.lastSuccess.Store(time.Now().Unix())
		return true
	}
	for {
		select {
		case <-w.stop:
			// Fixed total shutdown budget; never wait indefinitely for a failed database.
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			for ctx.Err() == nil {
				fill()
				if len(batch) == 0 || !write(ctx) {
					break
				}
			}
			cancel()
			w.dropped.Add(w.pending.Swap(0))
			w.pendingBytes.Store(0)
			return
		case now := <-ticker.C:
			if now.Before(next) {
				continue
			}
			fill()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			ok := write(ctx)
			cancel()
			if !ok {
				next = now.Add(backoff)
				backoff = min(30*time.Second, backoff*2)
				continue
			}
			backoff = time.Second
			if now.Sub(lastPrune) >= time.Minute {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				_ = w.sink.PruneEvents(ctx)
				cancel()
				lastPrune = now
			}
		}
	}
}
