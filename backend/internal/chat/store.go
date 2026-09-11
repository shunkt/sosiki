package chat

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shun/kaigi/backend/internal/persona"
	"github.com/shun/kaigi/backend/internal/retrieval"
)

type Message struct {
	ID        uuid.UUID `json:"id"`
	Role      string    `json:"role"` // "user" | "assistant"
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"createdAt"`
}

// Source is a citation as seen by the client: the persona-specific scores
// alongside the human-readable location, so the UI can show how much the
// persona moved each result rather than just the blended rank.
type Source struct {
	ChunkID    uuid.UUID `json:"chunkId"`
	DocumentID uuid.UUID `json:"documentId"`
	Title      string    `json:"title"`
	URL        string    `json:"url"`
	Relevance  float32   `json:"relevance"`
	Affinity   float32   `json:"affinity"`
}

type Conversation struct {
	ID        uuid.UUID
	PersonaID uuid.UUID
	Title     string
	Messages  []Message
}

type Store struct {
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

func (s *Store) CreateConversation(ctx context.Context, personaID uuid.UUID, title string) (uuid.UUID, error) {
	var id uuid.UUID
	if err := s.pool.QueryRow(ctx, `
		INSERT INTO conversations (persona_id, title) VALUES ($1, $2) RETURNING id
	`, personaID, title).Scan(&id); err != nil {
		return uuid.Nil, fmt.Errorf("chat: create conversation: %w", err)
	}
	return id, nil
}

// PersonaFor loads the persona a conversation belongs to, interests included,
// so the caller can drive query rewriting and reranking without a second
// round trip through persona.Store.
func (s *Store) PersonaFor(ctx context.Context, conversationID uuid.UUID) (persona.Persona, error) {
	var p persona.Persona
	var verbosity string
	if err := s.pool.QueryRow(ctx, `
		SELECT p.id, p.name, p.stance, p.verbosity, p.skepticism
		FROM conversations c
		JOIN personas p ON p.id = c.persona_id
		WHERE c.id = $1
	`, conversationID).Scan(&p.ID, &p.Name, &p.Personality.Stance, &verbosity, &p.Personality.Skepticism); err != nil {
		if err == pgx.ErrNoRows {
			return persona.Persona{}, fmt.Errorf("chat: %w", ErrConversationNotFound)
		}
		return persona.Persona{}, fmt.Errorf("chat: load persona for conversation: %w", err)
	}
	p.Personality.Verbosity = persona.Verbosity(verbosity)

	rows, err := s.pool.Query(ctx, `
		SELECT topic, weight FROM persona_interests WHERE persona_id = $1
	`, p.ID)
	if err != nil {
		return persona.Persona{}, fmt.Errorf("chat: load interests: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var in persona.Interest
		if err := rows.Scan(&in.Topic, &in.Weight); err != nil {
			return persona.Persona{}, fmt.Errorf("chat: scan interest: %w", err)
		}
		// Embeddings are not needed here: retrieval.Searcher recomputes
		// affinity against chunk vectors it already fetched, not against a
		// cached interest vector round-tripped through this call.
		p.Personality.Interests = append(p.Personality.Interests, in)
	}
	return p, rows.Err()
}

// History returns the last limit messages, oldest first, ready to append
// directly after the system prompt.
func (s *Store) History(ctx context.Context, conversationID uuid.UUID, limit int) ([]Message, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, role, content, created_at
		FROM messages
		WHERE conversation_id = $1
		ORDER BY seq DESC
		LIMIT $2
	`, conversationID, limit)
	if err != nil {
		return nil, fmt.Errorf("chat: history: %w", err)
	}
	defer rows.Close()

	var out []Message
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.Role, &m.Content, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("chat: scan message: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("chat: iterate messages: %w", err)
	}

	// Reverse: the query orders newest-first to LIMIT correctly, but the
	// caller needs chronological order.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// AppendMessage assigns the next seq for the conversation and inserts the
// message in one transaction, so concurrent appends cannot collide on seq.
func (s *Store) AppendMessage(ctx context.Context, conversationID uuid.UUID, role, content string) (Message, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Message{}, fmt.Errorf("chat: begin append: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var nextSeq int
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(MAX(seq), 0) + 1 FROM messages WHERE conversation_id = $1
	`, conversationID).Scan(&nextSeq); err != nil {
		return Message{}, fmt.Errorf("chat: next seq: %w", err)
	}

	var m Message
	m.Role = role
	m.Content = content
	if err := tx.QueryRow(ctx, `
		INSERT INTO messages (conversation_id, seq, role, content)
		VALUES ($1, $2, $3, $4)
		RETURNING id, created_at
	`, conversationID, nextSeq, role, content).Scan(&m.ID, &m.CreatedAt); err != nil {
		return Message{}, fmt.Errorf("chat: insert message: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return Message{}, fmt.Errorf("chat: commit append: %w", err)
	}
	return m, nil
}

// SaveCitations records which chunks backed a message, with both the raw
// relevance and the persona's affinity score preserved (see
// retrieval.Candidate) — not just the blended rank — so the citation trail
// survives even if PersonaInfluence is retuned later.
func (s *Store) SaveCitations(ctx context.Context, messageID uuid.UUID, candidates []retrieval.Candidate) error {
	if len(candidates) == 0 {
		return nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("chat: begin citations: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for rank, c := range candidates {
		if _, err := tx.Exec(ctx, `
			INSERT INTO message_citations (message_id, chunk_id, rank, relevance_score, affinity_score)
			VALUES ($1, $2, $3, $4, $5)
		`, messageID, c.ChunkID, rank, c.Relevance, c.Affinity); err != nil {
			return fmt.Errorf("chat: insert citation: %w", err)
		}
	}
	return tx.Commit(ctx)
}

// DocumentObjectKey resolves a documents.id to its MinIO object key, for the
// GET /api/documents/{id} redirect. It lives here rather than in a separate
// documents package since chat.Store is already the general-purpose
// Postgres access point for this domain and a single lookup does not
// justify a new package.
func (s *Store) DocumentObjectKey(ctx context.Context, id uuid.UUID) (string, error) {
	var key string
	if err := s.pool.QueryRow(ctx, `SELECT object_key FROM documents WHERE id = $1`, id).Scan(&key); err != nil {
		if err == pgx.ErrNoRows {
			return "", fmt.Errorf("chat: %w", ErrDocumentNotFound)
		}
		return "", fmt.Errorf("chat: document object key: %w", err)
	}
	return key, nil
}

func (s *Store) GetConversation(ctx context.Context, id uuid.UUID) (Conversation, error) {
	var c Conversation
	c.ID = id
	if err := s.pool.QueryRow(ctx, `
		SELECT persona_id, title FROM conversations WHERE id = $1
	`, id).Scan(&c.PersonaID, &c.Title); err != nil {
		if err == pgx.ErrNoRows {
			return Conversation{}, fmt.Errorf("chat: %w", ErrConversationNotFound)
		}
		return Conversation{}, fmt.Errorf("chat: get conversation: %w", err)
	}

	// 0 disables the LIMIT clause's usual role as a safety cap; a full
	// conversation reload legitimately wants everything.
	messages, err := s.History(ctx, id, 1_000_000)
	if err != nil {
		return Conversation{}, err
	}
	c.Messages = messages
	return c, nil
}

// ErrConversationNotFound lets API handlers map to 404 without matching on
// error text.
var ErrConversationNotFound = fmt.Errorf("conversation not found")

// ErrDocumentNotFound lets API handlers map to 404 without matching on
// error text.
var ErrDocumentNotFound = fmt.Errorf("document not found")
