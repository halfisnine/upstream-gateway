package interceptlog

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type fakeSink struct {
	mu      sync.Mutex
	events  []Event
	fail    bool
	entered chan struct{}
	release chan struct{}
}

func (s *fakeSink) WriteEvents(ctx context.Context, events []Event) error {
	if s.entered != nil {
		select {
		case s.entered <- struct{}{}:
		default:
		}
		select {
		case <-s.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return errors.New("offline")
	}
	s.events = append(s.events, events...)
	return nil
}
func (s *fakeSink) PruneEvents(context.Context) error { return nil }
func TestBoundedQueueAndFailureDoNotBlock(t *testing.T) {
	sink := &fakeSink{fail: true}
	w := New(sink)
	for i := 0; i < QueueSize+10; i++ {
		w.Enqueue(Event{ID: "test"})
	}
	stats := w.Stats()
	if stats.Pending != QueueSize || stats.Dropped != 10 {
		t.Fatal(stats)
	}
	w.Close()
	stats = w.Stats()
	if stats.Pending != 0 || stats.Dropped != QueueSize+10 || stats.Failures == 0 {
		t.Fatal(stats)
	}
	w.Enqueue(Event{})
	if w.Stats().Dropped != QueueSize+11 {
		t.Fatal("enqueue after close")
	}
}
func TestFlushAndMetadataLimit(t *testing.T) {
	s := &fakeSink{}
	w := New(s)
	for i := 0; i < 110; i++ {
		w.Enqueue(Event{ID: "test", RequestID: "reference"})
	}
	w.Enqueue(Event{Reason: string(make([]byte, MaxRecordBytes))})
	w.Close()
	if len(s.events) != 110 || w.Stats().Dropped != 1 {
		t.Fatal(len(s.events), w.Stats())
	}
}
func TestRetryBatchCountsAgainstCapacity(t *testing.T) {
	s := &fakeSink{entered: make(chan struct{}, 1), release: make(chan struct{})}
	w := New(s)
	for i := 0; i < QueueSize; i++ {
		w.Enqueue(Event{ID: "test"})
	}
	select {
	case <-s.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("writer did not run")
	}
	w.Enqueue(Event{ID: "excess"})
	if w.Stats().Pending != QueueSize || w.Stats().Dropped != 1 {
		t.Fatal(w.Stats())
	}
	close(s.release)
	w.Close()
}
func BenchmarkEnqueueAtCapacity(b *testing.B) {
	w := &Writer{queue: make(chan Event, QueueSize)}
	w.pending.Store(QueueSize)
	e := Event{ID: "event", RequestID: "request", Node: "node-a", Channel: "a", Protocol: "openai_chat", Time: time.Now()}
	b.ReportAllocs()
	for b.Loop() {
		w.Enqueue(e)
	}
}
