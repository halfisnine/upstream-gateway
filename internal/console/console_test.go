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
