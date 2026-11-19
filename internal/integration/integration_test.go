package integration

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/halfisnine/upstream-gateway/internal/config"
	"github.com/halfisnine/upstream-gateway/internal/console"
	"github.com/halfisnine/upstream-gateway/internal/gateway"
	"github.com/halfisnine/upstream-gateway/internal/store"
)

func database(t *testing.T) *store.Store {
	t.Helper()
	file := os.Getenv("GATEWAY_TEST_DATABASE_URL_FILE")
	if file == "" {
		t.Skip("isolated PostgreSQL integration requires GATEWAY_TEST_DATABASE_URL_FILE")
	}
	raw, e := os.ReadFile(file)
	if e != nil {
		t.Fatal("cannot read test database connection")
	}
	dsn := strings.TrimSpace(string(raw))
	root, e := store.Connect(dsn)
	if e != nil {
		t.Fatal("connect failed")
	}
	schema := "test_" + gateway.RandomID()
	if _, e = root.DB.Exec(`CREATE SCHEMA ` + schema); e != nil {
		t.Fatal("schema creation failed", e)
	}
	u, e := url.Parse(dsn)
	if e != nil {
		t.Fatal("invalid test connection")
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	s, e := store.Connect(u.String())
	if e != nil {
		t.Fatal("connect failed")
	}
	t.Cleanup(func() { s.Close(); root.DB.Exec(`DROP SCHEMA ` + schema + ` CASCADE`); root.Close() })
	if e = s.Init(context.Background()); e != nil {
		t.Fatal("migration failed", e)
	}
	return s
}
func cfg(base string) config.Config {
	return config.Config{Schema: config.Schema, InspectionTemplates: config.InspectionPresets(), Channels: []config.Channel{{ID: "a", Name: "测试渠道", InspectionTemplates: []string{"openai-chat", "gemini-generation"}, BaseURL: base, AllowHTTP: true, Protocols: map[string]config.Endpoint{"openai_chat": {}, "gemini": {}}}}}
}
func publish(t *testing.T, s *store.Store, key []byte, base int64, c config.Config) store.Version {
	t.Helper()
	data, digest, e := config.Seal(c, key)
	if e != nil {
		t.Fatal(e)
	}
	v, e := s.Publish(context.Background(), base, data, digest, "test")
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func TestDatabaseVersionsAndNodeLifecycle(t *testing.T) {
	s := database(t)
	key := bytes.Repeat([]byte{1}, 32)
	ctx := context.Background()
	v1 := publish(t, s, key, 0, cfg("http://127.0.0.1:1"))
	if _, err := s.DB.ExecContext(ctx, `UPDATE gateway_versions SET note='overwritten' WHERE id=1`); err == nil {
		t.Fatal("published snapshot could be mutated")
	}
	if _, err := s.DB.ExecContext(ctx, `DELETE FROM gateway_versions WHERE id=1`); err == nil {
		t.Fatal("published snapshot could be deleted")
	}
	a, b := gateway.New(s, key, "a"), gateway.New(s, key, "b")
	defer a.Close()
	defer b.Close()
	for _, n := range []*gateway.Node{a, b} {
		if e := n.Sync(ctx); e != nil {
			t.Fatal(e)
		}
		if n.Current().Version != v1.ID {
			t.Fatal("not synchronized")
		}
	}
	publish(t, s, key, 1, config.Empty())
	if e := a.Sync(ctx); e != nil {
		t.Fatal(e)
	}
	if a.Current().Version != 2 || b.Current().Version != 1 {
		t.Fatal("nodes did not retain independent applied versions")
	}
	old, e := s.Version(ctx, 1)
	if e != nil {
		t.Fatal(e)
	}
	oldCfg, e := config.Open(old.Data, old.Digest, key)
	if e != nil || len(oldCfg.Channels) != 1 {
		t.Fatal("historical snapshot was changed")
	}
	// A node with a wrong key retains its last good snapshot.
	b.Key = bytes.Repeat([]byte{2}, 32)
	if b.Sync(ctx) == nil || b.Current().Version != 1 || b.Status().Error != "snapshot_validation_failed" {
		t.Fatal("bad configuration displaced good snapshot")
	}
	clone := gateway.New(s, key, "a")
	defer clone.Close()
	if clone.Sync(ctx) != store.ErrNodeConflict || clone.Ready() {
		t.Fatal("duplicate node identity accepted")
	}
	nodes, e := s.Nodes(ctx)
	if e != nil || len(nodes) != 2 {
		t.Fatal(nodes, e)
	}
	// Restore V1 as V3, never move the head backwards.
	publish(t, s, key, 2, oldCfg)
	b.Key = key
	if e = b.Sync(ctx); e != nil || b.Current().Version != 3 || len(b.Current().Channels) != 1 {
		t.Fatal("restore failed", e)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			data, digest, _ := config.Seal(oldCfg, key)
			_, e := s.Publish(ctx, 3, data, digest, "concurrent")
			results <- e
		}()
	}
	wg.Wait()
	close(results)
	ok, conflict := 0, 0
	for e := range results {
		if e == nil {
			ok++
		} else if e == store.ErrConflict {
			conflict++
		} else {
			t.Fatal(e)
		}
	}
	if ok != 1 || conflict != 1 {
		t.Fatal("publication lost-update protection failed")
	}
	// Loss of the database cannot invalidate the already loaded data-plane snapshot.
	s.DB.Close()
	if b.Sync(ctx) == nil || !b.Ready() || b.Current().Version != 3 {
		t.Fatal("database failure removed live snapshot")
	}
}
func TestConsolePublicationSecurityAndHistory(t *testing.T) {
	s := database(t)
	key := bytes.Repeat([]byte{3}, 32)
	token := strings.Repeat("z", 32)
	n := gateway.New(s, key, "console")
	defer n.Close()
	srv := httptest.NewServer((&console.Console{Store: s, Key: key, Token: token, Node: n}).Handler())
	defer srv.Close()
	call := func(method, path string, body any, auth, origin string) (int, []byte) {
		t.Helper()
		var reader io.Reader
		if body != nil {
			raw, _ := json.Marshal(body)
			reader = bytes.NewReader(raw)
		}
		r, _ := http.NewRequest(method, srv.URL+path, reader)
		if auth != "" {
			r.Header.Set("Authorization", "Bearer "+auth)
		}
		r.Header.Set("Content-Type", "application/json")
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		res, e := http.DefaultClient.Do(r)
		if e != nil {
			t.Fatal(e)
		}
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		return res.StatusCode, raw
	}
	if code, _ := call("GET", "/api/status", nil, "", ""); code != 401 {
		t.Fatal("unprotected API")
	}
	body := map[string]any{"base_version": 0, "note": "first", "config": cfg("http://127.0.0.1:1")}
	if code, _ := call("POST", "/api/publish", body, token, "https://attacker.invalid"); code != 403 {
		t.Fatal("cross-site publication accepted")
	}
	if code, raw := call("POST", "/api/publish", body, token, ""); code != 201 {
		t.Fatal(code, string(raw))
	}
	if code, _ := call("POST", "/api/publish", body, token, ""); code != 409 {
		t.Fatal("conflicting baseline accepted")
	}
	code, raw := call("GET", "/api/versions/1", nil, token, "")
	if code != 200 || bytes.Contains(raw, []byte(`"key":`)) || bytes.Contains(raw, []byte(`"internal_token":`)) {
		t.Fatal("history revealed secrets", code)
	}
	var detail struct {
		Config config.Config `json:"config"`
	}
	if e := json.Unmarshal(raw, &detail); e != nil {
		t.Fatal(e)
	}
	detail.Config.Channels[0].Name = "modified"
	if code, raw = call("POST", "/api/publish", map[string]any{"base_version": 1, "note": "edit", "config": detail.Config}, token, ""); code != 201 {
		t.Fatal(code, string(raw))
	}
	v, e := s.Version(context.Background(), 2)
	if e != nil {
		t.Fatal(e)
	}
	full, e := config.Open(v.Data, v.Digest, key)
	if e != nil || full.Channels[0].Name != "modified" {
		t.Fatal("updated configuration was not persisted")
	}
	if code, _ = call("POST", "/api/rollback", map[string]any{"base_version": 2, "source_version": 1, "note": "restore"}, token, ""); code != 201 {
		t.Fatal(code)
	}
	v, _ = s.Version(context.Background(), 3)
	full, e = config.Open(v.Data, v.Digest, key)
	if e != nil || full.Channels[0].Name != "测试渠道" {
		t.Fatal("wrong restored contents")
	}
	if code, _ = call("GET", "/", nil, "", ""); code != 200 {
		t.Fatal("console unavailable")
	}
}
func TestTwoNodesUsePublishedRoutes(t *testing.T) {
	s := database(t)
	key := bytes.Repeat([]byte{4}, 32)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fake-provider-key" {
			t.Error("wrong key")
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"usage":{"total_tokens":3}}`)
	}))
	defer up.Close()
	publish(t, s, key, 0, cfg(up.URL))
	for _, id := range []string{"first", "second"} {
		n := gateway.New(s, key, id)
		defer n.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if e := n.Sync(ctx); e != nil {
			t.Fatal(e)
		}
		cancel()
		server := httptest.NewServer(n.Handler())
		defer server.Close()
		r, _ := http.NewRequest("POST", server.URL+"/r/a/v1/chat/completions", strings.NewReader(`{}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer fake-provider-key")
		res, e := http.DefaultClient.Do(r)
		if e != nil {
			t.Fatal(e)
		}
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != 200 || !bytes.Contains(b, []byte("total_tokens")) {
			t.Fatal("node did not forward published route")
		}
	}
}

func TestGracefulRestartReleasesOnlyOwnLease(t *testing.T) {
	s := database(t)
	key := bytes.Repeat([]byte{5}, 32)
	ctx := context.Background()
	publish(t, s, key, 0, config.Empty())
	original := gateway.New(s, key, "restart")
	if err := original.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	rejected := gateway.New(s, key, "restart")
	if rejected.Sync(ctx) != store.ErrNodeConflict {
		t.Fatal("duplicate accepted")
	}
	rejected.Close()
	nodes, err := s.Nodes(ctx)
	if err != nil || !nodes[0].Online {
		t.Fatal("rejected duplicate released owner")
	}
	original.Close()
	replacement := gateway.New(s, key, "restart")
	defer replacement.Close()
	if err := replacement.Sync(ctx); err != nil || !replacement.Ready() {
		t.Fatal("graceful restart cannot immediately claim its identity", err)
	}
	// Repeated cleanup of an old instance must not evict the replacement.
	original.Close()
	nodes, err = s.Nodes(ctx)
	if err != nil || !nodes[0].Online {
		t.Fatal("old cleanup evicted replacement")
	}
}

func TestLegacyHistoryConversionNeverRestoresCredentials(t *testing.T) {
	s := database(t)
	master := bytes.Repeat([]byte{8}, 32)
	ctx := context.Background()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer newapi-actual-key" {
			t.Error("legacy credentials replaced caller key")
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer upstream.Close()
	legacy := map[string]any{"schema_version": 2, "channels": []any{map[string]any{"id": "a", "name": "legacy", "base_url": upstream.URL, "allow_http": true, "key": "OLD_PROVIDER_SECRET", "internal_token": "OLD_INTERNAL_SECRET", "protocols": map[string]any{"openai_chat": map[string]any{}, "gemini": map[string]any{"key": "OLD_OVERRIDE_SECRET"}}}}}
	raw, _ := json.Marshal(legacy)
	block, _ := aes.NewCipher(master)
	g, _ := cipher.NewGCM(block)
	nonce := make([]byte, g.NonceSize())
	data := g.Seal(nonce, nonce, raw, []byte("upstream-gateway-schema-2"))
	sum := sha256.Sum256(data)
	original, err := s.Publish(ctx, 0, data, hex.EncodeToString(sum[:]), "legacy test fixture")
	if err != nil {
		t.Fatal(err)
	}
	n := gateway.New(s, master, "migrating")
	defer n.Close()
	if !errors.Is(n.Sync(ctx), config.ErrLegacyConfig) || n.Ready() || n.Status().Error != "legacy_configuration_requires_republish" {
		t.Fatal("legacy runtime activated without explicit publication")
	}
	adminToken := strings.Repeat("t", 32)
	server := httptest.NewServer((&console.Console{Store: s, Key: master, Token: adminToken, Node: n}).Handler())
	defer server.Close()
	request := func(method, path string, body []byte) (int, []byte) {
		r, _ := http.NewRequest(method, server.URL+path, bytes.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+adminToken)
		r.Header.Set("Content-Type", "application/json")
		res, e := http.DefaultClient.Do(r)
		if e != nil {
			t.Fatal(e)
		}
		defer res.Body.Close()
		payload, _ := io.ReadAll(res.Body)
		return res.StatusCode, payload
	}
	status, payload := request("GET", "/api/versions/1", nil)
	if status != 200 || bytes.Contains(payload, []byte("OLD_")) || bytes.Contains(payload, []byte(`"key":`)) || bytes.Contains(payload, []byte("internal_token")) {
		t.Fatal("legacy credentials appeared in API")
	}
	var detail struct {
		Config   config.Config `json:"config"`
		Source   int           `json:"source_schema_version"`
		Requires bool          `json:"requires_republish"`
	}
	if err = json.Unmarshal(payload, &detail); err != nil || detail.Source != 2 || !detail.Requires || detail.Config.Schema != config.Schema {
		t.Fatal("migration not visible", err)
	}
	status, _ = request("POST", "/api/rollback", []byte(`{"base_version":1,"source_version":1,"note":"convert without keys"}`))
	if status != 201 {
		t.Fatal(status)
	}
	if err = n.Sync(ctx); err != nil || !n.Ready() || n.Current().Version != 2 {
		t.Fatal("converted snapshot not activated", err)
	}
	updated, _ := s.Version(ctx, 2)
	plain, err := g.Open(nil, updated.Data[:g.NonceSize()], updated.Data[g.NonceSize():], []byte(fmt.Sprintf("upstream-gateway-schema-%d", config.Schema)))
	if err != nil {
		t.Fatal("cannot decrypt migrated snapshot", err)
	}
	if bytes.Contains(plain, []byte("OLD_")) || bytes.Contains(plain, []byte(`"key":`)) || bytes.Contains(plain, []byte("internal_token")) {
		t.Fatal("old credentials persisted in new snapshot")
	}
	kept, _ := s.Version(ctx, original.ID)
	if !bytes.Equal(kept.Data, data) {
		t.Fatal("immutable history overwritten")
	}
	bad := []byte(`{"base_version":2,"config":{"schema_version":3,"channels":[{"key":"DO_NOT_STORE"}]}}`)
	status, _ = request("POST", "/api/publish", bad)
	if status != 400 {
		t.Fatal("credential input accepted", status)
	}
	current, _ := s.Latest(ctx)
	if current != 2 {
		t.Fatal("rejected secret payload was published")
	}
	proxy := httptest.NewServer(n.Handler())
	defer proxy.Close()
	r, _ := http.NewRequest("POST", proxy.URL+"/r/a/v1/chat/completions", strings.NewReader(`{}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer newapi-actual-key")
	res, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatal("converted route did not pass caller credentials", res.StatusCode)
	}
}
