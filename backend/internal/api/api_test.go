package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/shun/kaigi/backend/internal/config"
)

func newTestHandler() http.Handler {
	cfg := config.Config{Addr: ":0", AllowedOrigins: []string{"http://localhost:5173"}}
	return NewHandler(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestHealth(t *testing.T) {
	rec := httptest.NewRecorder()
	newTestHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/health", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("status field = %q, want %q", body["status"], "ok")
	}
}

func TestHelloUsesNameQuery(t *testing.T) {
	rec := httptest.NewRecorder()
	newTestHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/hello?name=kaigi", nil))

	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if want := "hello, kaigi"; body["message"] != want {
		t.Errorf("message = %q, want %q", body["message"], want)
	}
}

func TestCORSOnlyEchoesAllowedOrigin(t *testing.T) {
	tests := []struct {
		name   string
		origin string
		want   string
	}{
		{"allowed", "http://localhost:5173", "http://localhost:5173"},
		{"denied", "http://evil.example", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
			req.Header.Set("Origin", tt.origin)
			rec := httptest.NewRecorder()
			newTestHandler().ServeHTTP(rec, req)

			if got := rec.Header().Get("Access-Control-Allow-Origin"); got != tt.want {
				t.Errorf("Access-Control-Allow-Origin = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStatusRecorderSupportsFlush(t *testing.T) {
	rec := &statusRecorder{ResponseWriter: httptest.NewRecorder(), status: http.StatusOK}

	// Without Unwrap the controller cannot find the Flusher and SSE silently
	// buffers every token until the handler returns.
	if err := http.NewResponseController(rec).Flush(); err != nil {
		t.Fatalf("Flush through statusRecorder: %v", err)
	}
}

func TestPreflightReturnsNoContent(t *testing.T) {
	req := httptest.NewRequest(http.MethodOptions, "/api/health", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	rec := httptest.NewRecorder()
	newTestHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
}
