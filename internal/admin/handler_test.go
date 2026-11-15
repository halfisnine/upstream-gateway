package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// A live bootstrap must not be reported ready or silently accept model traffic.
func TestBootstrapDoesNotAcceptModelRequests(t *testing.T) {
	for _, tc := range []struct {
		method string
		path   string
		status int
	}{
		{http.MethodGet, "/healthz", http.StatusOK},
		{http.MethodGet, "/readyz", http.StatusServiceUnavailable},
		{http.MethodPost, "/healthz", http.StatusMethodNotAllowed},
		{http.MethodPost, "/v1/chat/completions", http.StatusNotFound},
		{http.MethodPost, "/v1/messages", http.StatusNotFound},
		{http.MethodPost, "/r/example/v1/chat/completions", http.StatusNotFound},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			Handler().ServeHTTP(response, httptest.NewRequest(tc.method, tc.path, nil))
			if response.Code != tc.status {
				t.Fatalf("status = %d, want %d", response.Code, tc.status)
			}
		})
	}
}
