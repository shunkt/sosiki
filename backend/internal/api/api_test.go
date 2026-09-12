package api

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/shun/kaigi/backend/internal/chat"
	"github.com/shun/kaigi/backend/internal/config"
)

func testConfig() config.Config {
	return config.Config{
		Addr:           ":0",
		AllowedOrigins: []string{"http://localhost:5173"},
	}
}

// newTestHandler wires no Pool, so handleHealth reports db as
// "unconfigured" and the overall status as "degraded". A live probe is
// exercised manually against the docker-compose stack.
func newTestHandler() http.Handler {
	return NewHandler(testConfig(), Deps{
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
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
	// No Pool wired (see newTestHandler) makes "degraded" the correct answer
	// here — a live check is exercised manually against the docker-compose
	// stack per the plan's Task 1 VALIDATE.
	if body["status"] != "degraded" {
		t.Errorf("status field = %q, want %q", body["status"], "degraded")
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

// --- SSE handler tests (Task 11) ---

// fakeChatEngine drives handleSendMessage's frame-by-frame behavior without
// a live OpenAI/Postgres/MinIO stack.
type fakeChatEngine struct {
	events []chat.Event
	err    error
}

func (f fakeChatEngine) Reply(_ context.Context, _ uuid.UUID, _ string, out chan<- chat.Event) error {
	for _, ev := range f.events {
		out <- ev
	}
	return f.err
}

func newSSETestHandler(engine fakeChatEngine) http.Handler {
	return NewHandler(testConfig(), Deps{
		Log:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Chat: engine,
	})
}

func TestSSEFramesAreSeparate(t *testing.T) {
	engine := fakeChatEngine{events: []chat.Event{
		{Type: "token", Text: "a"},
		{Type: "token", Text: "b"},
		{Type: "token", Text: "c"},
	}}
	srv := httptest.NewServer(newSSETestHandler(engine))
	t.Cleanup(srv.Close)

	resp, err := http.Post(srv.URL+"/api/conversations/"+uuid.NewString()+"/messages",
		"application/json", strings.NewReader(`{"content":"hi"}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	var tokenFrames int
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event: token") {
			tokenFrames++
		}
	}
	// If Flush weren't reaching the client (the statusRecorder.Unwrap bug
	// this whole feature guards against), all three tokens would arrive as
	// one frame instead of three.
	if tokenFrames != 3 {
		t.Errorf("got %d separate token frames, want 3", tokenFrames)
	}
}

func TestSSEEncodesNewlinesInTokens(t *testing.T) {
	engine := fakeChatEngine{events: []chat.Event{
		{Type: "token", Text: "line one\nline two"},
	}}
	srv := httptest.NewServer(newSSETestHandler(engine))
	t.Cleanup(srv.Close)

	resp, err := http.Post(srv.URL+"/api/conversations/"+uuid.NewString()+"/messages",
		"application/json", strings.NewReader(`{"content":"hi"}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	var dataLines int
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		if strings.HasPrefix(scanner.Text(), "data: ") {
			dataLines++
			var ev chat.Event
			if err := json.Unmarshal([]byte(strings.TrimPrefix(scanner.Text(), "data: ")), &ev); err != nil {
				t.Fatalf("data line is not valid JSON (a raw newline in the token corrupted the frame): %v", err)
			}
		}
	}
	if dataLines != 1 {
		t.Errorf("got %d data lines, want 1 (a raw newline would split it into more)", dataLines)
	}
}

func TestSendMessageRejectsEmptyContent(t *testing.T) {
	srv := httptest.NewServer(newSSETestHandler(fakeChatEngine{}))
	t.Cleanup(srv.Close)

	resp, err := http.Post(srv.URL+"/api/conversations/"+uuid.NewString()+"/messages",
		"application/json", strings.NewReader(`{"content":""}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestSendMessageRejectsInvalidConversationID(t *testing.T) {
	srv := httptest.NewServer(newSSETestHandler(fakeChatEngine{}))
	t.Cleanup(srv.Close)

	resp, err := http.Post(srv.URL+"/api/conversations/not-a-uuid/messages",
		"application/json", strings.NewReader(`{"content":"hi"}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestSSEEmitsErrorEventOnEngineFailure(t *testing.T) {
	engine := fakeChatEngine{err: io.ErrUnexpectedEOF}
	srv := httptest.NewServer(newSSETestHandler(engine))
	t.Cleanup(srv.Close)

	resp, err := http.Post(srv.URL+"/api/conversations/"+uuid.NewString()+"/messages",
		"application/json", strings.NewReader(`{"content":"hi"}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	var gotError bool
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		if strings.HasPrefix(scanner.Text(), "event: error") {
			gotError = true
		}
	}
	if !gotError {
		t.Error("expected an error event when the engine returns an error")
	}
}
