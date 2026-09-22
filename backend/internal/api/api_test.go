package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/shun/kaigi/backend/internal/config"
	"github.com/shun/kaigi/backend/internal/meeting"
	"github.com/shun/kaigi/backend/internal/registry"
)

var errFakeModerator = errors.New("fake moderator failure")

func testConfig() config.Config {
	return config.Config{
		Addr:           ":0",
		AllowedOrigins: []string{"http://localhost:5173"},
	}
}

type fakeAgentLister struct {
	agents []registry.AgentDTO
	err    error
}

func (f fakeAgentLister) ListAgents(context.Context) ([]registry.AgentDTO, error) {
	return f.agents, f.err
}

// newTestHandler wires no Pool, so handleHealth reports db as
// "unconfigured" and the overall status as "degraded". A live probe is
// exercised manually against the docker-compose stack.
func newTestHandler() http.Handler {
	return NewHandler(testConfig(), Deps{
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Agents: fakeAgentLister{},
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

func TestListPersonas(t *testing.T) {
	h := NewHandler(testConfig(), Deps{
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Agents: fakeAgentLister{agents: []registry.AgentDTO{
			{Slug: "critic", Name: "批評家", Present: true},
		}},
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/personas", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var agents []registry.AgentDTO
	if err := json.NewDecoder(rec.Body).Decode(&agents); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(agents) != 1 || agents[0].Slug != "critic" {
		t.Errorf("agents = %+v, want 1 entry with slug critic", agents)
	}
}

// TestListPersonasPassesThroughProfile guards that handleListPersonas is a
// pure pass-through of the discovery pod's AgentDTO (see internal/api/personas.go)
// — the persona directory's profile field must reach the browser unmodified,
// and a nil profile must serialize as JSON null rather than being dropped.
func TestListPersonasPassesThroughProfile(t *testing.T) {
	h := NewHandler(testConfig(), Deps{
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Agents: fakeAgentLister{agents: []registry.AgentDTO{
			{
				Slug: "critic", Name: "批評家", Present: true,
				Profile: &registry.Profile{
					Stance:     "根拠のない主張には懐疑的",
					Skepticism: 0.85,
					Verbosity:  "concise",
					Interests:  []registry.ProfileInterest{{Topic: "Raft", Weight: 0.6}},
				},
			},
			{Slug: "pragmatist", Name: "実務家", Present: false, Profile: nil},
		}},
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/personas", nil))

	var body []map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode raw: %v", err)
	}
	if len(body) != 2 {
		t.Fatalf("agents = %d entries, want 2", len(body))
	}
	if string(body[0]["profile"]) == "" {
		t.Fatal("critic's \"profile\" key is missing from the JSON response")
	}
	if string(body[0]["profile"]) == "null" {
		t.Error("critic's profile serialized as null, want the profile object")
	}
	if string(body[1]["profile"]) != "null" {
		t.Errorf("pragmatist's profile = %s, want null", body[1]["profile"])
	}

	var agents []registry.AgentDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &agents); err != nil {
		t.Fatalf("decode typed: %v", err)
	}
	if agents[0].Profile == nil || agents[0].Profile.Skepticism != 0.85 {
		t.Errorf("agents[0].Profile = %+v, want Skepticism 0.85", agents[0].Profile)
	}
	if agents[1].Profile != nil {
		t.Errorf("agents[1].Profile = %+v, want nil", agents[1].Profile)
	}
}

func TestCreateMeetingRejectsAbsentPersona(t *testing.T) {
	h := NewHandler(testConfig(), Deps{
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Agents: fakeAgentLister{agents: []registry.AgentDTO{
			{Slug: "critic", Name: "批評家", Present: false},
		}},
	})

	body := strings.NewReader(`{"topic":"x","personaSlugs":["critic"]}`)
	req := httptest.NewRequest(http.MethodPost, "/api/meetings", body)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestCreateMeetingRejectsUnknownPersona(t *testing.T) {
	h := NewHandler(testConfig(), Deps{
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Agents: fakeAgentLister{agents: nil},
	})

	body := strings.NewReader(`{"topic":"x","personaSlugs":["ghost"]}`)
	req := httptest.NewRequest(http.MethodPost, "/api/meetings", body)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestCreateMeetingRejectsEmptyParticipants(t *testing.T) {
	h := newTestHandler()
	body := strings.NewReader(`{"topic":"x","personaSlugs":[]}`)
	req := httptest.NewRequest(http.MethodPost, "/api/meetings", body)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

// --- SSE handler tests ---

// fakeModerator drives handleSendTurn's frame-by-frame behavior without
// live persona pods.
type fakeModerator struct {
	events []meeting.Event
	err    error
}

func (f fakeModerator) Run(_ context.Context, _ uuid.UUID, _ string, _ int, out chan<- meeting.Event) error {
	for _, ev := range f.events {
		out <- ev
	}
	return f.err
}

func newSSETestHandler(mod fakeModerator) http.Handler {
	return NewHandler(testConfig(), Deps{
		Log:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		Agents:        fakeAgentLister{},
		Moderator:     mod,
		DefaultRounds: 1,
	})
}

func TestSendTurnFramesAreSeparate(t *testing.T) {
	mod := fakeModerator{events: []meeting.Event{
		{Type: "token", PersonaSlug: "critic", Text: "a"},
		{Type: "token", PersonaSlug: "critic", Text: "b"},
		{Type: "token", PersonaSlug: "critic", Text: "c"},
	}}
	srv := httptest.NewServer(newSSETestHandler(mod))
	t.Cleanup(srv.Close)

	resp, err := http.Post(srv.URL+"/api/meetings/"+uuid.NewString()+"/turns",
		"application/json", strings.NewReader(`{"content":"hi"}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	var tokenFrames int
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		if strings.HasPrefix(scanner.Text(), "event: token") {
			tokenFrames++
		}
	}
	if tokenFrames != 3 {
		t.Errorf("got %d separate token frames, want 3", tokenFrames)
	}
}

func TestSendTurnEncodesNewlinesInTokens(t *testing.T) {
	mod := fakeModerator{events: []meeting.Event{
		{Type: "token", PersonaSlug: "critic", Text: "line one\nline two"},
	}}
	srv := httptest.NewServer(newSSETestHandler(mod))
	t.Cleanup(srv.Close)

	resp, err := http.Post(srv.URL+"/api/meetings/"+uuid.NewString()+"/turns",
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
			var ev meeting.Event
			if err := json.Unmarshal([]byte(strings.TrimPrefix(scanner.Text(), "data: ")), &ev); err != nil {
				t.Fatalf("data line is not valid JSON (a raw newline in the token corrupted the frame): %v", err)
			}
		}
	}
	if dataLines != 1 {
		t.Errorf("got %d data lines, want 1 (a raw newline would split it into more)", dataLines)
	}
}

func TestSendTurnRejectsEmptyContent(t *testing.T) {
	srv := httptest.NewServer(newSSETestHandler(fakeModerator{}))
	t.Cleanup(srv.Close)

	resp, err := http.Post(srv.URL+"/api/meetings/"+uuid.NewString()+"/turns",
		"application/json", strings.NewReader(`{"content":""}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestSendTurnRejectsInvalidMeetingID(t *testing.T) {
	srv := httptest.NewServer(newSSETestHandler(fakeModerator{}))
	t.Cleanup(srv.Close)

	resp, err := http.Post(srv.URL+"/api/meetings/not-a-uuid/turns",
		"application/json", strings.NewReader(`{"content":"hi"}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestSendTurnEmitsErrorEventOnModeratorFailure(t *testing.T) {
	mod := fakeModerator{err: errFakeModerator}
	srv := httptest.NewServer(newSSETestHandler(mod))
	t.Cleanup(srv.Close)

	resp, err := http.Post(srv.URL+"/api/meetings/"+uuid.NewString()+"/turns",
		"application/json", strings.NewReader(`{"content":"hi"}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	var gotErrorFrame bool
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		if strings.HasPrefix(scanner.Text(), "event: error") {
			gotErrorFrame = true
		}
	}
	if !gotErrorFrame {
		t.Error("expected an error frame when the moderator returns an error")
	}
}
