package chat

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/shun/kaigi/backend/internal/persona"
)

func testPersona() persona.Persona {
	return persona.Persona{
		Name: "批評家",
		Personality: persona.Personality{
			Stance: "根拠のない主張には懐疑的",
			Interests: []persona.Interest{
				{Topic: "形式的検証", Weight: 0.8},
				{Topic: "マーケティング", Weight: -0.5},
			},
			Skepticism: 0.85,
			Verbosity:  persona.VerbosityDetailed,
		},
	}
}

// TestBuildSystemPromptIsStable guards the OpenAI automatic-caching
// contract: the same persona+participants must render to byte-identical
// prompts across calls, since nothing marks a cache boundary explicitly — a
// stable prefix is the only lever.
func TestBuildSystemPromptIsStable(t *testing.T) {
	p := testPersona()
	a := BuildSystemPrompt(p, []string{"批評家", "実務家"})
	b := BuildSystemPrompt(p, []string{"批評家", "実務家"})
	if a != b {
		t.Errorf("BuildSystemPrompt is not stable across calls:\n%q\nvs\n%q", a, b)
	}
}

// TestBuildSystemPromptStableAcrossParticipantOrder is the meeting-specific
// half of the caching contract: participant order comes from
// speaking_order, which is not guaranteed to be sorted, so two calls with
// the same set of participants in different orders must still cache-hit.
func TestBuildSystemPromptStableAcrossParticipantOrder(t *testing.T) {
	p := testPersona()
	a := BuildSystemPrompt(p, []string{"批評家", "実務家", "設計者"})
	b := BuildSystemPrompt(p, []string{"設計者", "批評家", "実務家"})
	if a != b {
		t.Errorf("BuildSystemPrompt varies with participant order:\n%q\nvs\n%q", a, b)
	}
}

func TestBuildSystemPromptSingleParticipant(t *testing.T) {
	p := testPersona()
	got := BuildSystemPrompt(p, []string{"批評家"})
	if strings.Contains(got, "複数人の会議です") {
		t.Errorf("prompt mentions a meeting with a single participant:\n%s", got)
	}
}

func TestBuildSystemPromptMentionsOtherParticipants(t *testing.T) {
	p := testPersona()
	got := BuildSystemPrompt(p, []string{"批評家", "実務家"})
	if !strings.Contains(got, "実務家") {
		t.Errorf("prompt does not mention the other participant:\n%s", got)
	}
	if strings.Contains(got, "他の参加者: 批評家") {
		t.Errorf("prompt lists the persona itself as an other participant:\n%s", got)
	}
}

func TestBuildSystemPromptReflectsPersonality(t *testing.T) {
	p := testPersona()
	got := BuildSystemPrompt(p, nil)

	for _, want := range []string{"批評家", "形式的検証", "マーケティング", "留保"} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt does not mention %q:\n%s", want, got)
		}
	}
}

type fakeLLM struct {
	completion  ChatResponse
	completeErr error
}

func (f fakeLLM) CreateChatCompletion(context.Context, ChatRequest) (ChatResponse, error) {
	if f.completeErr != nil {
		return ChatResponse{}, f.completeErr
	}
	return f.completion, nil
}

func (f fakeLLM) CreateChatCompletionStream(context.Context, ChatRequest) (ChatStream, error) {
	return nil, errors.New("not implemented in this fake")
}

func TestRewriteQueriesParsesJSON(t *testing.T) {
	llm := fakeLLM{completion: ChatResponse{Content: `{"queries": ["合意形成", "Raft"]}`}}
	got := RewriteQueries(context.Background(), llm, "gpt-5-mini", testPersona(), nil, "合意形成について")
	want := []string{"合意形成", "Raft"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestRewriteQueriesFallsBackOnError(t *testing.T) {
	llm := fakeLLM{completeErr: errors.New("boom")}
	got := RewriteQueries(context.Background(), llm, "gpt-5-mini", testPersona(), nil, "元の発話")
	if len(got) != 1 || got[0] != "元の発話" {
		t.Errorf("got %v, want fallback to the raw utterance", got)
	}
}

func TestRewriteQueriesFallsBackOnInvalidJSON(t *testing.T) {
	llm := fakeLLM{completion: ChatResponse{Content: "not json at all"}}
	got := RewriteQueries(context.Background(), llm, "gpt-5-mini", testPersona(), nil, "元の発話")
	if len(got) != 1 || got[0] != "元の発話" {
		t.Errorf("got %v, want fallback to the raw utterance", got)
	}
}

func TestRewriteQueriesCapsAtMax(t *testing.T) {
	llm := fakeLLM{completion: ChatResponse{
		Content: `{"queries": ["a", "b", "c", "d", "e"]}`,
	}}
	got := RewriteQueries(context.Background(), llm, "gpt-5-mini", testPersona(), nil, "x")
	if len(got) != maxRewrittenQueries {
		t.Errorf("got %d queries, want capped at %d", len(got), maxRewrittenQueries)
	}
}
