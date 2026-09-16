package registry

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// Upsert registers or re-registers an agent, resetting last_seen_at to now.
// A persona pod calls this on every heartbeat (see registry.Client), so this
// is the only write this package needs — there is no separate Touch.
func (s *Store) Upsert(ctx context.Context, a Agent) error {
	cardJSON, err := json.Marshal(a.Card)
	if err != nil {
		return fmt.Errorf("registry: marshal card: %w", err)
	}
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO agents (slug, base_url, card, persona_id, last_seen_at)
		VALUES ($1, $2, $3, $4, now())
		ON CONFLICT (slug) DO UPDATE SET
			base_url = EXCLUDED.base_url,
			card = EXCLUDED.card,
			persona_id = EXCLUDED.persona_id,
			last_seen_at = now()
	`, a.Slug, a.BaseURL, cardJSON, a.PersonaID); err != nil {
		return fmt.Errorf("registry: upsert %s: %w", a.Slug, err)
	}
	return nil
}

// Get loads one agent by slug, including its stored last_seen_at (the
// caller decides presence via Agent.Present, since that needs a ttl and a
// notion of "now" this package does not own).
func (s *Store) Get(ctx context.Context, slug string) (Agent, error) {
	var a Agent
	var cardJSON []byte
	a.Slug = slug
	if err := s.pool.QueryRow(ctx, `
		SELECT base_url, card, persona_id, last_seen_at FROM agents WHERE slug = $1
	`, slug).Scan(&a.BaseURL, &cardJSON, &a.PersonaID, &a.LastSeenAt); err != nil {
		if err == pgx.ErrNoRows {
			return Agent{}, ErrNotFound
		}
		return Agent{}, fmt.Errorf("registry: get %s: %w", slug, err)
	}
	var card a2a.AgentCard
	if err := json.Unmarshal(cardJSON, &card); err != nil {
		return Agent{}, fmt.Errorf("registry: decode card for %s: %w", slug, err)
	}
	a.Card = &card
	return a, nil
}

// List returns every registered agent, ordered by slug for a stable UI
// listing (the moderator's /api/personas endpoint renders this directly).
func (s *Store) List(ctx context.Context) ([]Agent, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT slug, base_url, card, persona_id, last_seen_at FROM agents ORDER BY slug
	`)
	if err != nil {
		return nil, fmt.Errorf("registry: list: %w", err)
	}
	defer rows.Close()

	var out []Agent
	for rows.Next() {
		var a Agent
		var cardJSON []byte
		if err := rows.Scan(&a.Slug, &a.BaseURL, &cardJSON, &a.PersonaID, &a.LastSeenAt); err != nil {
			return nil, fmt.Errorf("registry: scan: %w", err)
		}
		var card a2a.AgentCard
		if err := json.Unmarshal(cardJSON, &card); err != nil {
			return nil, fmt.Errorf("registry: decode card for %s: %w", a.Slug, err)
		}
		a.Card = &card
		out = append(out, a)
	}
	return out, rows.Err()
}
