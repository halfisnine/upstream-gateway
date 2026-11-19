package console

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/halfisnine/upstream-gateway/internal/gateway"
)

func TestAssetsAndAuthorization(t *testing.T) {
	n := gateway.New(nil, nil, "test")
	defer n.Close()
	handler := (&Console{Token: strings.Repeat("x", 32), Node: n}).Handler()
	for _, tc := range []struct {
		method, path string
		code         int
	}{{"GET", "/", 200}, {"GET", "/app.js", 200}, {"GET", "/style.css", 200}, {"GET", "/readyz", 503}, {"GET", "/api/status", 401}, {"POST", "/api/publish", 401}, {"POST", "/", 405}, {"GET", "/not-found", 404}} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
		if w.Code != tc.code {
			t.Fatalf("%s %s: %d", tc.method, tc.path, w.Code)
		}
		if w.Header().Get("Content-Security-Policy") == "" || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("missing protection headers at %s %s: %v", tc.method, tc.path, w.Header())
		}
	}
	r := httptest.NewRequest(http.MethodPost, "/api/publish", strings.NewReader(`{}`))
	r.Header.Set("Authorization", "Bearer "+strings.Repeat("x", 32))
	r.Header.Set("Origin", "https://evil.invalid")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("cross origin accepted")
	}
}

func TestRuleSampleAPIUsesRealMatcherWithoutUpstream(t *testing.T) {
	n := gateway.New(nil, nil, "test")
	defer n.Close()
	token := strings.Repeat("x", 32)
	h := (&Console{Token: token, Node: n}).Handler()
	for _, path := range []string{"/api/inspection-presets", "/api/inspection-test", "/api/error-presets", "/api/interceptions", "/api/interceptions/test/original"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 401 {
			t.Fatal(path, w.Code)
		}
	}
	raw := `{"config":{"schema_version":7,"inspect_embedded_errors":true,"channels":[{"id":"a","name":"A","base_url":"https://example.invalid","protocols":{"openai_chat":{}}}],"error_rules":[{"id":"balance","name":"余额","enabled":true,"sources":["http","sse"],"return_status":503,"message":"SAFE"}]},"sample":{"channel":"a","protocol":"openai_chat","source":"sse","status":200,"committed":true,"error":{"message":"PRIVATE"}}}`
	req := httptest.NewRequest("POST", "/api/error-test", strings.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"actual_status":200`) || !strings.Contains(w.Body.String(), `"status_override_skipped":true`) || strings.Contains(w.Body.String(), "PRIVATE") {
		t.Fatal(w.Code, w.Body.String())
	}
}
