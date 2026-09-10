package chat

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"

	"github.com/shun/kaigi/backend/internal/config"
	"github.com/shun/kaigi/backend/internal/objectstore"
	"github.com/shun/kaigi/backend/internal/persona"
	"github.com/shun/kaigi/backend/internal/retrieval"
)

// The interfaces below are narrow seams over Store, retrieval.Searcher, and
// objectstore.Store — the same pattern retrieval.Searcher uses for its own
// TEI clients — so Reply's event ordering (sources -> token* -> done) can be
// unit tested with fakes and no live Postgres or MinIO.
type conversationStore interface {
	PersonaFor(ctx context.Context, conversationID uuid.UUID) (persona.Persona, error)
	History(ctx context.Context, conversationID uuid.UUID, limit int) ([]Message, error)
	AppendMessage(ctx context.Context, conversationID uuid.UUID, role, content string) (Message, error)
	SaveCitations(ctx context.Context, messageID uuid.UUID, candidates []retrieval.Candidate) error
}

type knowledgeSearcher interface {
	Search(ctx context.Context, p persona.Persona, queries []string) ([]retrieval.Candidate, error)
}

type contextExpander interface {
	ExpandContext(ctx context.Context, key string, start, end, size int64, pad int) (string, error)
	PresignedURL(ctx context.Context, key string, ttl time.Duration) (string, error)
}

// Event is one unit of progress the HTTP layer turns into an SSE frame.
type Event struct {
	Type    string   `json:"type"` // "token" | "sources" | "done" | "error"
	Text    string   `json:"text,omitempty"`
	Sources []Source `json:"sources,omitempty"`
	Message *Message `json:"message,omitempty"`
	Error   string   `json:"error,omitempty"`
}

// presignedURLTTL is how long a citation link handed to the frontend stays
// valid. Long enough to survive a slow page load, short enough that a link
// copied out of the app doesn't work indefinitely.
const presignedURLTTL = 15 * time.Minute

type Engine struct {
	llm      LLMClient
	searcher knowledgeSearcher
	objects  contextExpander
	store    conversationStore
	cfg      config.Config

	// historyTurns bounds how much prior conversation is loaded and sent to
	// the model each turn. Compaction is out of scope for this feature; a
	// fixed cutoff is the simple stand-in — see the plan's Notes.
	historyTurns int
	// contextConcurrency bounds parallel MinIO reads when expanding chunks.
	contextConcurrency int
}

func NewEngine(llm LLMClient, s *retrieval.Searcher, objects *objectstore.Store, store *Store, cfg config.Config) *Engine {
	return &Engine{
		llm:                llm,
		searcher:           s,
		objects:            objects,
		store:              store,
		cfg:                cfg,
		historyTurns:       20,
		contextConcurrency: 4,
	}
}

// Reply runs one turn and pushes events to out, closing it when done. It
// owns the whole turn: it persists both the user message and the assistant
// reply itself, so a client that disconnects mid-stream does not lose the
// conversation.
func (e *Engine) Reply(ctx context.Context, conversationID uuid.UUID, utterance string, out chan<- Event) error {
	p, err := e.store.PersonaFor(ctx, conversationID)
	if err != nil {
		return fmt.Errorf("reply: load persona: %w", err)
	}

	history, err := e.store.History(ctx, conversationID, e.historyTurns)
	if err != nil {
		return fmt.Errorf("reply: load history: %w", err)
	}

	// Saved before generation starts: if the stream fails or the client
	// disconnects, the user's message is not lost.
	if _, err := e.store.AppendMessage(ctx, conversationID, "user", utterance); err != nil {
		return fmt.Errorf("reply: save user message: %w", err)
	}

	queries := RewriteQueries(ctx, e.llm, e.cfg.LLM.Model, p, history, utterance)

	candidates, err := e.searcher.Search(ctx, p, queries)
	if err != nil {
		return fmt.Errorf("reply: search: %w", err)
	}

	expanded, sources, err := e.expandContext(ctx, candidates)
	if err != nil {
		return fmt.Errorf("reply: expand context: %w", err)
	}
	out <- Event{Type: "sources", Sources: sources}

	systemPrompt := BuildSystemPrompt(p)
	messages := make([]ChatMessage, 0, len(history)+2)
	messages = append(messages, ChatMessage{Role: "system", Content: systemPrompt})
	for _, m := range history {
		messages = append(messages, ChatMessage{Role: m.Role, Content: m.Content})
	}
	// The documents block rides in the user turn, not system: system stays
	// byte-identical across turns (see BuildSystemPrompt) so DeepSeek's
	// automatic prefix caching keeps working; putting per-turn content there
	// would invalidate the cache on every message.
	messages = append(messages, ChatMessage{Role: "user", Content: expanded + utterance})

	reply, err := e.stream(ctx, conversationID, messages, out)
	if err != nil {
		return err
	}

	// A dropped client cancels ctx, but the reply the model already produced
	// should still be saved — detach from the request's cancellation for the
	// persistence step only.
	saveCtx := context.WithoutCancel(ctx)
	assistantMsg, err := e.store.AppendMessage(saveCtx, conversationID, "assistant", reply)
	if err != nil {
		return fmt.Errorf("reply: save assistant message: %w", err)
	}
	if err := e.store.SaveCitations(saveCtx, assistantMsg.ID, candidates); err != nil {
		return fmt.Errorf("reply: save citations: %w", err)
	}

	out <- Event{Type: "done", Message: &assistantMsg}
	return nil
}

func (e *Engine) stream(ctx context.Context, conversationID uuid.UUID, messages []ChatMessage, out chan<- Event) (string, error) {
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
			out <- Event{Type: "token", Text: choice.Content}
		}
		if choice.FinishReason == "length" {
			slog.Warn("reply truncated by max output", "conversation_id", conversationID)
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
			URL:        url,
			Relevance:  c.Relevance,
			Affinity:   c.Affinity,
		}
	}
	return block.String(), sources, nil
}
