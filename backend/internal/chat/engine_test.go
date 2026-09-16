package chat

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/shun/kaigi/backend/internal/config"
	"github.com/shun/kaigi/backend/internal/persona"
	"github.com/shun/kaigi/backend/internal/retrieval"
)

// --- fakes ---

type fakeSearcher struct {
	candidates []retrieval.Candidate
}

func (f fakeSearcher) Search(context.Context, persona.Persona, []string) ([]retrieval.Candidate, error) {
	return f.candidates, nil
}

type fakeObjects struct{}

func (fakeObjects) ExpandContext(context.Context, string, int64, int64, int64, int) (string, error) {
	return "expanded context", nil
}

func (fakeObjects) PresignedURL(context.Context, string, time.Duration) (string, error) {
	return "https://example.invalid/doc", nil
}

// fakeLLMStream drives the engine's streaming loop with a scripted sequence
// of chunks, so tests control exactly what CreateChatCompletionStream sees
// without a live OpenAI connection.
type fakeLLMStream struct {
	llm     ChatResponse // used for RewriteQueries's non-streaming call
	chunks  []ChatStreamChunk
	pos     int
	recvErr error // returned once, after all chunks are exhausted
}

func (f *fakeLLMStream) CreateChatCompletion(context.Context, ChatRequest) (ChatResponse, error) {
	return f.llm, nil
}

func (f *fakeLLMStream) CreateChatCompletionStream(context.Context, ChatRequest) (ChatStream, error) {
	return f, nil
}

func (f *fakeLLMStream) Recv() (ChatStreamChunk, error) {
	if f.pos >= len(f.chunks) {
		if f.recvErr != nil {
			return ChatStreamChunk{}, f.recvErr
		}
		return ChatStreamChunk{}, io.EOF
	}
	c := f.chunks[f.pos]
	f.pos++
	return c, nil
}

func (f *fakeLLMStream) Close() error { return nil }

func testEngine(llm *fakeLLMStream, cands []retrieval.Candidate) *Engine {
	e := NewEngine(llm, nil, nil, config.Config{
		LLM:       config.LLMConfig{Model: "gpt-5-mini"},
		Retrieval: config.RetrievalConfig{ContextPadBytes: 100},
	})
	e.searcher = fakeSearcher{candidates: cands}
	e.objects = fakeObjects{}
	return e
}

func TestEngineEmitsEventOrder(t *testing.T) {
	p := persona.Persona{Name: "test"}
	cand := retrieval.Candidate{ChunkID: uuid.New(), DocumentID: uuid.New(), Title: "doc"}
	llm := &fakeLLMStream{
		llm: ChatResponse{Content: `{"queries": ["q"]}`},
		chunks: []ChatStreamChunk{
			{Choices: []ChatStreamChoice{{Content: "こん"}}},
			{Choices: []ChatStreamChoice{{Content: "にちは"}}},
		},
	}
	e := testEngine(llm, []retrieval.Candidate{cand})

	out := make(chan Event, 10)
	if err := e.Reply(context.Background(), p, []string{"test"}, nil, "hi", out); err != nil {
		t.Fatalf("Reply: %v", err)
	}
	close(out)

	var events []Event
	for ev := range out {
		events = append(events, ev)
	}

	if len(events) != 4 {
		t.Fatalf("got %d events, want 4 (sources, token, token, done): %+v", len(events), events)
	}
	if events[0].Type != "sources" {
		t.Errorf("event[0].Type = %q, want sources", events[0].Type)
	}
	if events[1].Type != "token" || events[1].Text != "こん" {
		t.Errorf("event[1] = %+v, want token こん", events[1])
	}
	if events[2].Type != "token" || events[2].Text != "にちは" {
		t.Errorf("event[2] = %+v, want token にちは", events[2])
	}
	if events[3].Type != "done" {
		t.Errorf("event[3] = %+v, want done", events[3])
	}
	if events[3].Text != "こんにちは" {
		t.Errorf("done reply text = %q, want こんにちは", events[3].Text)
	}
	if len(events[3].Candidates) != 1 {
		t.Errorf("done candidates = %d, want 1 (the caller persists these, not Engine)", len(events[3].Candidates))
	}
}

func TestEngineSkipsEmptyChoices(t *testing.T) {
	p := persona.Persona{Name: "test"}
	llm := &fakeLLMStream{
		llm: ChatResponse{Content: `{"queries": ["q"]}`},
		chunks: []ChatStreamChunk{
			{Choices: nil}, // e.g. a usage-only chunk
			{Choices: []ChatStreamChoice{{Content: "ok"}}},
		},
	}
	e := testEngine(llm, nil)

	out := make(chan Event, 10)
	if err := e.Reply(context.Background(), p, []string{"test"}, nil, "hi", out); err != nil {
		t.Fatalf("Reply: %v", err)
	}
	close(out)

	var tokens []string
	for ev := range out {
		if ev.Type == "token" {
			tokens = append(tokens, ev.Text)
		}
	}
	if len(tokens) != 1 || tokens[0] != "ok" {
		t.Errorf("tokens = %v, want [ok] (empty-choices chunk should be skipped, not panic)", tokens)
	}
}

func TestEngineReturnsErrorOnStreamFailure(t *testing.T) {
	p := persona.Persona{Name: "test"}
	llm := &fakeLLMStream{
		llm:     ChatResponse{Content: `{"queries": ["q"]}`},
		recvErr: errors.New("connection reset"),
	}
	e := testEngine(llm, nil)

	out := make(chan Event, 10)
	err := e.Reply(context.Background(), p, []string{"test"}, nil, "hi", out)
	if err == nil {
		t.Fatal("expected an error from Reply when the stream fails")
	}
}

func TestEngineHandlesNoCandidates(t *testing.T) {
	p := persona.Persona{Name: "test"}
	llm := &fakeLLMStream{
		llm:    ChatResponse{Content: `{"queries": ["q"]}`},
		chunks: []ChatStreamChunk{{Choices: []ChatStreamChoice{{Content: "no sources"}}}},
	}
	e := testEngine(llm, nil) // nil candidates: everything was filtered by skepticism

	out := make(chan Event, 10)
	if err := e.Reply(context.Background(), p, []string{"test"}, nil, "hi", out); err != nil {
		t.Fatalf("Reply: %v", err)
	}
	close(out)

	var gotSources bool
	for ev := range out {
		if ev.Type == "sources" {
			gotSources = true
			if len(ev.Sources) != 0 {
				t.Errorf("Sources = %v, want empty", ev.Sources)
			}
		}
	}
	if !gotSources {
		t.Error("expected a sources event even with zero candidates")
	}
}

// TestEngineUsesTranscriptForOtherSpeakers exercises the 【speaker】 wrapping
// that lets a persona see a meeting's earlier turns from other speakers —
// this is the mechanism meeting.Moderator relies on for personas to react to
// each other's statements (see the plan's TestModeratorRounds).
func TestEngineUsesTranscriptForOtherSpeakers(t *testing.T) {
	p := persona.Persona{Name: "実務家"}
	llm := &fakeLLMStream{
		llm:    ChatResponse{Content: `{"queries": ["q"]}`},
		chunks: []ChatStreamChunk{{Choices: []ChatStreamChoice{{Content: "ok"}}}},
	}
	e := testEngine(llm, nil)

	transcript := []Turn{
		{Role: "user", SpeakerName: "user", Content: "議題です"},
		{Role: "persona", SpeakerName: "批評家", Content: "批評家の発言です"},
		{Role: "persona", SpeakerName: "実務家", Content: "自分の前の発言です"},
	}

	out := make(chan Event, 10)
	if err := e.Reply(context.Background(), p, []string{"批評家", "実務家"}, transcript, "続き", out); err != nil {
		t.Fatalf("Reply: %v", err)
	}
	close(out)
	for range out {
	}

	// Indirect check: toChatMessages is what actually builds the LLM-bound
	// history. Exercise it directly for a precise assertion on role mapping.
	msgs := toChatMessages(p.Name, transcript)
	if len(msgs) != 3 {
		t.Fatalf("got %d messages, want 3", len(msgs))
	}
	if msgs[0].Role != "user" {
		t.Errorf("human turn role = %q, want user", msgs[0].Role)
	}
	if msgs[1].Role != "user" {
		t.Errorf("other persona's turn role = %q, want user (not assistant)", msgs[1].Role)
	}
	if msgs[2].Role != "assistant" {
		t.Errorf("own prior turn role = %q, want assistant", msgs[2].Role)
	}
}
