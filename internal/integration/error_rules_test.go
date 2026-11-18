package integration

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/halfisnine/upstream-gateway/internal/console"
	"github.com/halfisnine/upstream-gateway/internal/errorrules"
	"github.com/halfisnine/upstream-gateway/internal/gateway"
	"github.com/halfisnine/upstream-gateway/internal/interceptlog"
	"github.com/halfisnine/upstream-gateway/internal/store"
)

func TestInterceptionStoragePaginationRetention(t *testing.T) {
	s := database(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	events := []interceptlog.Event{}
	for i := 0; i < 4; i++ {
		events = append(events, interceptlog.Event{ID: fmt.Sprintf("event-%d", i), RequestID: fmt.Sprintf("request-%d", i), Time: now, Channel: "a", Node: "node-a", Actual: 503, Version: 1, RuleID: "balance"})
	}
	if e := s.WriteEvents(ctx, events); e != nil {
		t.Fatal(e)
	}
	if e := s.WriteEvents(ctx, events); e != nil {
		t.Fatal(e)
	}
	q := store.EventQuery{Since: now.Add(-time.Hour), Until: now, Limit: 2}
	page, e := s.Events(ctx, q)
	if e != nil || len(page.Events) != 2 || page.Next == "" {
		t.Fatal(page, e)
	}
	first := page.Events[0].ID
	q.Cursor = page.Next
	second, e := s.Events(ctx, q)
	if e != nil || len(second.Events) != 2 || second.Next != "" || second.Events[0].ID == first {
		t.Fatal(second, e)
	}
	q.Cursor = ""
	q.RequestID = "request-1"
	page, e = s.Events(ctx, q)
	if e != nil || len(page.Events) != 1 {
		t.Fatal(page, e)
	}
	q.RequestID = ""
	q.Status = 429
	page, e = s.Events(ctx, q)
	if e != nil || len(page.Events) != 0 {
		t.Fatal(page, e)
	}
	q.Cursor = "bad"
	if _, e = s.Events(ctx, q); e == nil {
		t.Fatal("bad cursor accepted")
	}
	if _, e = s.DB.Exec(`TRUNCATE gateway_interceptions`); e != nil {
		t.Fatal(e)
	}
	if _, e = s.DB.Exec(`INSERT INTO gateway_interceptions(id,occurred_at,request_id,channel,rule_id,node,actual_status,event) SELECT 'seed-'||i,now()-i*interval '1 second','request','a','','node-a',502,'{}'::jsonb FROM generate_series(1,50004) i`); e != nil {
		t.Fatal(e)
	}
	if e = s.PruneEvents(ctx); e != nil {
		t.Fatal(e)
	}
	var count int
	if e = s.DB.QueryRow(`SELECT count(*) FROM gateway_interceptions`).Scan(&count); e != nil || count != 50000 {
		t.Fatal(count, e)
	}
	if _, e = s.DB.Exec(`UPDATE gateway_interceptions SET occurred_at=now()-interval '4 days' WHERE id='seed-1'; UPDATE gateway_log_maintenance SET last_run='epoch'`); e != nil {
		t.Fatal(e)
	}
	if e = s.PruneEvents(ctx); e != nil {
		t.Fatal(e)
	}
	if e = s.DB.QueryRow(`SELECT count(*) FROM gateway_interceptions WHERE id='seed-1'`).Scan(&count); e != nil || count != 0 {
		t.Fatal(count, e)
	}
}
func TestLogFailureDoesNotBlockProxyOrConfigSync(t *testing.T) {
	s := database(t)
	key := []byte(strings.Repeat("k", 32))
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(403)
		io.WriteString(w, `{"error":{"message":"余额不足 PRIVATE"}}`)
	}))
	defer up.Close()
	c := cfg(up.URL)
	c.InspectEmbeddedErrors = true
	c.ErrorRules = errorrules.Presets()
	publish(t, s, key, 0, c)
	n := gateway.New(s, key, "log-node")
	defer n.Close()
	if e := n.Sync(context.Background()); e != nil {
		t.Fatal(e)
	}
	gw := httptest.NewServer(n.Handler())
	defer gw.Close()
	if _, e := s.DB.Exec(`ALTER TABLE gateway_interceptions RENAME TO unavailable_logs`); e != nil {
		t.Fatal(e)
	}
	defer s.DB.Exec(`ALTER TABLE IF EXISTS unavailable_logs RENAME TO gateway_interceptions`)
	for i := 0; i < 3; i++ {
		req, _ := http.NewRequest("POST", gw.URL+"/r/a/v1/chat/completions", strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer test-key")
		res, e := http.DefaultClient.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		raw, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != 503 || strings.Contains(string(raw), "PRIVATE") {
			t.Fatal(res.StatusCode, string(raw))
		}
	}
	deadline := time.Now().Add(4 * time.Second)
	for n.Logs.Stats().Failures == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if n.Logs.Stats().Failures == 0 {
		t.Fatal("log outage not reported")
	}
	if e := n.Sync(context.Background()); e != nil || !n.Ready() {
		t.Fatal("log failure blocked sync", e)
	}
	nodes, e := s.Nodes(context.Background())
	if e != nil || nodes[0].Logs.Failures == 0 || nodes[0].Logs.Instance != n.Instance {
		t.Fatal(nodes, e)
	}
	if _, e = s.DB.Exec(`ALTER TABLE unavailable_logs RENAME TO gateway_interceptions`); e != nil {
		t.Fatal(e)
	}
	n.Logs.Close()
	page, e := s.Events(context.Background(), store.EventQuery{Since: time.Now().Add(-time.Hour), Until: time.Now(), Limit: 50})
	if e != nil || len(page.Events) != 3 {
		t.Fatal(page, e)
	}
	for _, event := range page.Events {
		if event.RuleID != "balance" || event.Version != 1 || event.Desired != 503 {
			t.Fatal(event)
		}
	}
}

func TestOriginalStorageAndAdminAccess(t *testing.T) {
	s := database(t)
	ctx := context.Background()
	key := []byte(strings.Repeat("s", 32))
	event := interceptlog.Event{ID: "original-event", Time: time.Now().UTC(), RequestID: "request", Actual: 502, Channel: "a", Node: "test"}
	event.CaptureOriginal(key, []byte(`{"error":{"message":"RAW_ERROR_ADMIN_ONLY","api_key":"SECRET"}}`), "", false)
	if e := s.WriteEvents(ctx, []interceptlog.Event{event}); e != nil {
		t.Fatal(e)
	}
	// Migration is repeatable and does not lose existing ciphertext.
	if e := s.Init(ctx); e != nil {
		t.Fatal(e)
	}
	var stored string
	if e := s.DB.QueryRow(`SELECT event::text FROM gateway_interceptions WHERE id=$1`, event.ID).Scan(&stored); e != nil {
		t.Fatal(e)
	}
	if strings.Contains(stored, "RAW_ERROR_ADMIN_ONLY") || strings.Contains(stored, "SECRET") {
		t.Fatal("plaintext at rest")
	}
	page, e := s.Events(ctx, store.EventQuery{Since: time.Now().Add(-time.Hour), Until: time.Now(), Limit: 50})
	if e != nil || len(page.Events) != 1 || len(page.Events[0].OriginalCipher) != 0 {
		t.Fatal("list exposed cipher", e)
	}
	node := gateway.New(s, key, "original-test")
	defer node.Close()
	token := strings.Repeat("a", 32)
	handler := (&console.Console{Store: s, Key: key, Token: token, Node: node}).Handler()
	get := func(id, auth string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/api/interceptions/"+id+"/original", nil)
		if auth != "" {
			r.Header.Set("Authorization", "Bearer "+auth)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if get(event.ID, "").Code != 401 {
		t.Fatal("unauthenticated original")
	}
	w := get(event.ID, token)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "RAW_ERROR_ADMIN_ONLY") || strings.Contains(w.Body.String(), "SECRET") || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("admin original unavailable", w.Code)
	}
	legacy := event
	legacy.ID = "legacy-event"
	legacy.OriginalCipher = nil
	legacy.OriginalState = ""
	if e = s.WriteEvents(ctx, []interceptlog.Event{legacy}); e != nil {
		t.Fatal(e)
	}
	if w = get(legacy.ID, token); w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"legacy"`) {
		t.Fatal("legacy original not distinguished")
	}
	_, _ = s.DB.Exec(`DELETE FROM gateway_interceptions WHERE id=$1`, event.ID)
	if get(event.ID, token).Code != 404 {
		t.Fatal("retention did not remove original")
	}
}
