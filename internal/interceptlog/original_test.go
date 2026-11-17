package interceptlog

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestOriginalEncryptionRedactionAndBinding(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	e := Event{ID: "first"}
	raw := []byte(`{"error":{"message":"bad CURRENT_SECRET insufficient balance","api_key":"OTHER_SECRET","nested":{"refresh_token":"REFRESH"}},"header":"Bearer ABC_TOKEN"}`)
	e.CaptureOriginal(key, raw, "CURRENT_SECRET", false)
	body, err := e.OpenOriginal(key)
	if err != nil || e.OriginalState != "captured" || !e.OriginalRedacted || e.OriginalTruncated || !strings.Contains(body, "insufficient balance") {
		t.Fatal(e.OriginalState, err)
	}
	for _, secret := range []string{"CURRENT_SECRET", "OTHER_SECRET", "REFRESH", "ABC_TOKEN"} {
		if strings.Contains(body, secret) || bytes.Contains(e.OriginalCipher, []byte(secret)) {
			t.Fatal("credential retained")
		}
	}
	wire, _ := json.Marshal(e)
	if bytes.Contains(wire, []byte("insufficient balance")) {
		t.Fatal("original persisted in plaintext")
	}
	e.ID = "second"
	if _, err = e.OpenOriginal(key); err == nil {
		t.Fatal("cipher moved to another event")
	}
	e.ID = "first"
	if _, err = e.OpenOriginal(bytes.Repeat([]byte{8}, 32)); err == nil {
		t.Fatal("wrong key accepted")
	}
	e.OriginalCipher[len(e.OriginalCipher)-1] ^= 1
	if _, err = e.OpenOriginal(key); err == nil {
		t.Fatal("tampered original accepted")
	}
}
func TestOriginalBoundariesAndUnavailable(t *testing.T) {
	key := bytes.Repeat([]byte{4}, 32)
	for _, raw := range [][]byte{[]byte(strings.Repeat("中", 1000)), []byte(strings.Repeat("x", MaxOriginalBytes-5) + "SECRET_CROSSING_EDGE"), []byte(`{"api_key":"` + strings.Repeat("K", 10000))} {
		e := Event{ID: "test"}
		e.CaptureOriginal(key, raw, "SECRET_CROSSING_EDGE", false)
		text, err := e.OpenOriginal(key)
		if err != nil || !e.OriginalTruncated || len(text) > MaxOriginalBytes || !utf8.ValidString(text) || strings.Contains(text, "SECRE") || strings.Contains(text, "KKKK") {
			t.Fatal("bad truncation or redaction", err)
		}
	}
	e := Event{ID: "test"}
	e.CaptureOriginal(key, []byte{}, "", false)
	text, err := e.OpenOriginal(key)
	if e.OriginalState != "captured" || text != "" || err != nil {
		t.Fatal("empty body")
	}
	e = Event{ID: "test"}
	e.CaptureOriginal(key, nil, "", false)
	if e.OriginalState != "unavailable" {
		t.Fatal(e.OriginalState)
	}
	e = Event{ID: "test"}
	e.CaptureOriginal(nil, []byte("private"), "", false)
	if e.OriginalState != "encryption_failed" || len(e.OriginalCipher) != 0 {
		t.Fatal("unprotected fallback")
	}
	e = Event{ID: "test"}
	e.CaptureOriginal(key, []byte("partial"), "", true)
	if !e.OriginalTruncated {
		t.Fatal("incomplete input not marked")
	}
}
func TestQueueBytesIncludeRetryBatch(t *testing.T) {
	sink := &fakeSink{fail: true}
	w := New(sink)
	defer w.Close()
	event := Event{ID: "event"}
	event.CaptureOriginal(bytes.Repeat([]byte{1}, 32), []byte(strings.Repeat("a", MaxOriginalBytes)), "", false)
	for i := 0; i < QueueSize; i++ {
		w.Enqueue(event)
	}
	stats := w.Stats()
	if stats.Pending >= QueueSize || stats.PendingBytes > QueueBytes || stats.Dropped == 0 {
		t.Fatal(stats)
	}
	w.Close()
	if w.Stats().PendingBytes != 0 {
		t.Fatal("byte budget not released")
	}
}
