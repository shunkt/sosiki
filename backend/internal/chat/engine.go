package chat

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/shun/kaigi/backend/internal/config"
	"github.com/shun/kaigi/backend/internal/objectstore"
	"github.com/shun/kaigi/backend/internal/persona"
	"github.com/shun/kaigi/backend/internal/retrieval"
)

// Turn is one prior statement in the meeting, as seen by a persona. It is
// passed in rather than loaded: a persona pod holds no conversation state
// (see the plan's "なぜペルソナ pod を会話ステートレスにするのか"), so the
// caller's transcript is the only history that exists for this Reply call.
type Turn struct {
	// SpeakerSlug is "" for the human's own turns. Matching self-vs-other by
	// slug (not SpeakerName) is what makes self-attribution correct even if
	// two personas happen to share a display name — only persona.Persona's
	// Slug (and personas.slug in the DB) is guaranteed unique.
	SpeakerSlug string
	SpeakerName string // "user" for the human, the persona's own Name otherwise
	Role        string // "user" | "persona"
	Content     string
}

// The interfaces below are narrow seams over retrieval.Searcher and
// objectstore.Store — the same pattern retrieval.Searcher uses for its own
// embeddings client — so Reply's event ordering (sources -> token* -> done)
// can be unit tested with fakes and no live OpenAI, Postgres, or MinIO.
type knowledgeSearcher interface {
	Search(ctx context.Context, p persona.Persona, queries []string) ([]retrieval.Candidate, error)
}

type contextExpander interface {
	ExpandContext(ctx context.Context, key string, start, end, size int64, pad int) (string, error)
	PresignedURL(ctx context.Context, key string, ttl time.Duration) (string, error)
}

// Event is one unit of progress the caller turns into an SSE frame (directly,
// for a single-persona conversation, or wrapped into a meeting.Event by
// personaexec/moderator). Persistence is the caller's job — unlike the
// pre-A2A engine, Reply does not save anything itself: a persona pod has no
// conversation database to save to (see the plan's DB-per-function split).
type Event struct {
	Type    string   `json:"type"` // "token" | "sources" | "done" | "error"
	Text    string   `json:"text,omitempty"`
	Sources []Source `json:"sources,omitempty"`
	// Candidates carries the full retrieval.Candidate (chunk id, scores, MinIO
	// location) on "done" so the caller can persist citations however its own
	// storage layer wants — Source above is presign-resolved for a client,
	// Candidates is the raw material for that.
	Candidates []retrieval.Candidate `json:"-"`
	Error      string                `json:"error,omitempty"`
}

// presignedURLTTL is how long a citation link handed to the frontend stays
// valid. Long enough to survive a slow page load, short enough that a link
// copied out of the app doesn't work indefinitely.
const presignedURLTTL = 15 * time.Minute

type Engine struct {
	llm      LLMClient
	searcher knowledgeSearcher
	objects  contextExpander
	cfg      config.Config

	// contextConcurrency bounds parallel MinIO reads when expanding chunks.
	contextConcurrency int
}

func NewEngine(llm LLMClient, s *retrieval.Searcher, objects *objectstore.Store, cfg config.Config) *Engine {
	return &Engine{
		llm:                llm,
		searcher:           s,
		objects:            objects,
		cfg:                cfg,
		contextConcurrency: 4,
	}
}

// Reply runs one turn for persona p against the transcript so far and pushes
// events to out. It does not close out (the caller owns that, since the
// caller may be multiplexing several personas onto one SSE stream — see
// meeting.Moderator) and it does not persist anything: the caller decides
// where the resulting message and citations live.
func (e *Engine) Reply(ctx context.Context, p persona.Persona, participants []string, transcript []Turn, utterance string, out chan<- Event) error {
	history := toChatMessages(p.Slug, transcript)

	queries := RewriteQueries(ctx, e.llm, e.cfg.LLM.Model, p, history, utterance)

	candidates, err := e.searcher.Search(ctx, p, queries)
	if err != nil {
		return fmt.Errorf("reply: search: %w", err)
	}

	expanded, sources, err := e.expandContext(ctx, candidates)
	if err != nil {
		return fmt.Errorf("reply: expand context: %w", err)
	}
	if !sendEvent(ctx, out, Event{Type: "sources", Sources: sources}) {
		return ctx.Err()
	}

	systemPrompt := BuildSystemPrompt(p, participants)
	messages := make([]ChatMessage, 0, len(transcript)+2)
	messages = append(messages, ChatMessage{Role: "system", Content: systemPrompt})
	// The meeting transcript can include statements from other personas, not
	// just a strict user/assistant back-and-forth. Only this persona's own
	// prior statements map to "assistant" — everything else (the human, and
	// every other persona) rides as "user" with a 【speaker】 prefix so the
	// model never has to guess who said what.
	for _, t := range transcript {
		if t.Role == "persona" && t.SpeakerSlug == p.Slug {
			messages = append(messages, ChatMessage{Role: "assistant", Content: t.Content})
			continue
		}
		messages = append(messages, ChatMessage{
			Role:    "user",
			Content: fmt.Sprintf("【%s】%s", t.SpeakerName, t.Content),
		})
	}
	// The documents block rides in the final user turn, not system: system
	// stays byte-identical across turns for the same persona+participants
	// (see BuildSystemPrompt) so OpenAI's automatic prefix caching keeps
	// working; putting per-turn content there would invalidate the cache on
	// every message.
	messages = append(messages, ChatMessage{Role: "user", Content: expanded + utterance})

	reply, err := e.stream(ctx, messages, out)
	if err != nil {
		return err
	}

	sendEvent(ctx, out, Event{Type: "done", Text: reply, Candidates: candidates})
	return nil
}

// sendEvent sends ev on out, but bails out via ctx instead of blocking
// forever if the consumer has stopped reading (e.g. an A2A client that
// disconnected mid-stream — see personaexec.Executor.Execute, which cancels
// its runCtx in exactly that case) and the buffered channel is full. A
// plain `out <- ev` here would leak this goroutine for the life of the
// process: ctx cancellation only unblocks operations that explicitly check
// it (like the OpenAI stream's Recv below), never a channel send already
// parked on a full buffer. Caught by an actual code review, not by
// TestExecutorYieldFalseCancelsEngine — that test's fake happened to select
// on ctx.Done() when sending, which the real implementation did not.
func sendEvent(ctx context.Context, out chan<- Event, ev Event) bool {
	select {
	case out <- ev:
		return true
	case <-ctx.Done():
		return false
	}
}

// toChatMessages renders the transcript into the shape RewriteQueries expects
// for pronoun resolution — see prompt.go's buildRewritePrompt, which only
// needs a short flattened role/content history, not the full 【speaker】
// disambiguation that the generation prompt below needs.
func toChatMessages(selfSlug string, transcript []Turn) []Message {
	out := make([]Message, len(transcript))
	for i, t := range transcript {
		role := "user"
		if t.Role == "persona" && t.SpeakerSlug == selfSlug {
			role = "assistant"
		}
		out[i] = Message{Role: role, Content: t.Content}
	}
	return out
}

func (e *Engine) stream(ctx context.Context, messages []ChatMessage, out chan<- Event) (string, error) {
	stream, err := e.llm.CreateChatCompletionStream(ctx, ChatRequest{
		Model:    e.cfg.LLM.Model,
		Stream:   true,
		Messages: messages,
	})
	if err != nil {
		return "", fmt.Errorf("reply: start stream: %w", err)
	}
	defer stream.Close()

	var sb strings.Builder
	for {
		chunk, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("reply: stream recv: %w", err)
		}
		// OpenAI-compatible streams interleave usage-only chunks that carry
		// no choices; indexing Choices[0] unconditionally would panic on those.
		if len(chunk.Choices) == 0 {
			continue
		}
		choice := chunk.Choices[0]
		if choice.Content != "" {
			sb.WriteString(choice.Content)
			if !sendEvent(ctx, out, Event{Type: "token", Text: choice.Content}) {
				return "", ctx.Err()
			}
		}
		if choice.FinishReason == "length" {
			slog.Warn("reply truncated by max output")
		}
	}
	return sb.String(), nil
}

// expandContext reads the surrounding text for each candidate from MinIO in
// parallel — Range GETs are independent per chunk — and builds both the
// prompt block and the client-facing source list from the same pass.
func (e *Engine) expandContext(ctx context.Context, candidates []retrieval.Candidate) (string, []Source, error) {
	if len(candidates) == 0 {
		return "", nil, nil
	}

	expansions := make([]string, len(candidates))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(e.contextConcurrency)
	for i, c := range candidates {
		g.Go(func() error {
			text, err := e.objects.ExpandContext(gctx, c.ObjectKey, c.ByteStart, c.ByteEnd, c.SizeBytes, e.cfg.Retrieval.ContextPadBytes)
			if err != nil {
				return fmt.Errorf("expand chunk %s: %w", c.ChunkID, err)
			}
			expansions[i] = text
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return "", nil, err
	}

	var block strings.Builder
	sources := make([]Source, len(candidates))
	for i, c := range candidates {
		fmt.Fprintf(&block, "<資料 id=\"%d\" title=\"%s\">\n%s\n</資料>\n", i+1, c.Title, expansions[i])

		url, err := e.objects.PresignedURL(ctx, c.ObjectKey, presignedURLTTL)
		if err != nil {
			return "", nil, fmt.Errorf("presign %s: %w", c.ObjectKey, err)
		}
		sources[i] = Source{
			ChunkID:    c.ChunkID,
			DocumentID: c.DocumentID,
			Title:      c.Title,
			ObjectKey:  c.ObjectKey,
			URL:        url,
			Relevance:  c.Relevance,
			Affinity:   c.Affinity,
		}
	}
	return block.String(), sources, nil
}
