package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/shun/kaigi/backend/internal/persona"
)

// BuildSystemPrompt renders the persona's personality into the stable prefix
// of every request in a conversation. DeepSeek caches automatically on a
// matching prefix — there is no explicit cache_control — so this string must
// be byte-identical across turns for the same persona: no timestamps, no
// conversation ID, nothing that varies.
func BuildSystemPrompt(p persona.Persona) string {
	var b strings.Builder

	fmt.Fprintf(&b, "あなたは「%s」というペルソナとして会話します。", p.Name)
	if p.Personality.Stance != "" {
		fmt.Fprintf(&b, "\n立場: %s", p.Personality.Stance)
	}

	if len(p.Personality.Interests) > 0 {
		var favor, disfavor []string
		for _, in := range p.Personality.Interests {
			if in.Weight >= 0 {
				favor = append(favor, in.Topic)
			} else {
				disfavor = append(disfavor, in.Topic)
			}
		}
		if len(favor) > 0 {
			fmt.Fprintf(&b, "\n重視する観点: %s", strings.Join(favor, "、"))
		}
		if len(disfavor) > 0 {
			fmt.Fprintf(&b, "\n重視しない観点: %s", strings.Join(disfavor, "、"))
		}
	}

	if p.Personality.Skepticism >= 0.7 {
		b.WriteString("\n根拠が薄い主張には明示的に留保を付けてください。")
	}

	switch p.Personality.Verbosity {
	case persona.VerbosityConcise:
		b.WriteString("\n回答は3文以内で簡潔に述べてください。")
	case persona.VerbosityDetailed:
		b.WriteString("\n回答は背景や反論も含めて詳しく述べてください。")
	default:
		b.WriteString("\n回答は必要十分な長さで述べてください。")
	}

	b.WriteString("\n与えられた資料に基づいて答え、該当箇所を [1] のように参照してください。" +
		"資料にないことは推測せず、その旨を述べてください。")

	return b.String()
}

// rewriteQueriesJSON is the shape DeepSeek's JSON Output is asked to produce.
// The exact response_format parameter and json_schema support are unverified
// (see the plan's Notes) — the shape is enforced by parsing, not by a schema,
// so a malformed response degrades to the fallback below rather than erroring.
type rewriteQueriesJSON struct {
	Queries []string `json:"queries"`
}

const maxRewrittenQueries = 3

// RewriteQueries turns the user's utterance into retrieval queries the
// persona would actually ask, biased by Stance and Interests. On any
// failure — request error or unparsable JSON — it returns the utterance
// itself as a single query so a rewrite failure never blocks retrieval.
func RewriteQueries(ctx context.Context, llm LLMClient, model string, p persona.Persona, history []Message, utterance string) []string {
	fallback := []string{utterance}

	prompt := buildRewritePrompt(p, history)
	resp, err := llm.CreateChatCompletion(ctx, ChatRequest{
		Model:    model,
		JSONMode: true,
		Messages: []ChatMessage{
			{Role: "system", Content: prompt},
			{Role: "user", Content: utterance},
		},
	})
	if err != nil {
		slog.Warn("query rewrite failed, falling back to raw utterance", "error", err)
		return fallback
	}

	var parsed rewriteQueriesJSON
	if err := json.Unmarshal([]byte(resp.Content), &parsed); err != nil {
		slog.Warn("query rewrite returned unparsable JSON, falling back to raw utterance",
			"error", err, "content", resp.Content)
		return fallback
	}
	if len(parsed.Queries) == 0 {
		slog.Warn("query rewrite returned zero queries, falling back to raw utterance")
		return fallback
	}
	if len(parsed.Queries) > maxRewrittenQueries {
		parsed.Queries = parsed.Queries[:maxRewrittenQueries]
	}
	return parsed.Queries
}

// recentHistoryForRewrite bounds how much prior conversation the rewrite
// prompt carries. Only enough to resolve pronouns like "それ" — the full
// history is unnecessary context for a task that produces search queries,
// not a reply.
const recentHistoryForRewrite = 4

func buildRewritePrompt(p persona.Persona, history []Message) string {
	var b strings.Builder
	fmt.Fprintf(&b, "あなたは「%s」というペルソナです。", p.Name)
	if p.Personality.Stance != "" {
		fmt.Fprintf(&b, "立場: %s。", p.Personality.Stance)
	}
	if len(p.Personality.Interests) > 0 {
		topics := make([]string, len(p.Personality.Interests))
		for i, in := range p.Personality.Interests {
			topics[i] = in.Topic
		}
		fmt.Fprintf(&b, "関心のある観点: %s。", strings.Join(topics, "、"))
	}

	if n := len(history); n > 0 {
		start := max(0, n-recentHistoryForRewrite)
		b.WriteString("\n直前の会話:\n")
		for _, m := range history[start:] {
			fmt.Fprintf(&b, "%s: %s\n", m.Role, m.Content)
		}
	}

	b.WriteString("ユーザーの直近の発話を読み、このペルソナが知識ベースを検索するとしたら" +
		"どんなクエリで調べるかを考え、最大3件の検索クエリを日本語で挙げてください。" +
		"「それ」「これ」などの指示語は直前の会話から具体的な語に置き換えてください。")
	b.WriteString(`必ず次のJSON形式のみで出力してください: {"queries": ["クエリ1", "クエリ2"]}`)
	return b.String()
}
