package persona

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	pgvector "github.com/pgvector/pgvector-go"
)

// embedder is the narrow seam retrieval.Embedder satisfies, kept local so
// this package does not import retrieval (which would create a cycle: search
// needs persona.Persona, so persona cannot need retrieval.Searcher).
type embedder interface {
	EmbedQueries(ctx context.Context, texts []string) ([][]float32, error)
}

type Store struct {
	pool     *pgxpool.Pool
	embedder embedder
}

func NewStore(pool *pgxpool.Pool, embedder embedder) *Store {
	return &Store{pool: pool, embedder: embedder}
}

type CreateInput struct {
	Name       string
	Stance     string
	Verbosity  Verbosity
	Skepticism float32
	Interests  []InterestInput
}

type InterestInput struct {
	Topic  string
	Weight float32
}

// Create embeds each interest's topic before storing it. Interests are
// embedded with the same query prefix used at retrieval time (see
// retrieval.EmbedQueries) so affinity is a fair cosine comparison against
// chunk embeddings, which are embedded as documents.
func (s *Store) Create(ctx context.Context, in CreateInput) (Persona, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Persona{}, fmt.Errorf("persona: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	verbosity := in.Verbosity
	if verbosity == "" {
		verbosity = VerbosityBalanced
	}

	var id uuid.UUID
	if err := tx.QueryRow(ctx, `
		INSERT INTO personas (name, stance, verbosity, skepticism)
		VALUES ($1, $2, $3, $4)
		RETURNING id
	`, in.Name, in.Stance, string(verbosity), in.Skepticism).Scan(&id); err != nil {
		return Persona{}, fmt.Errorf("persona: insert: %w", err)
	}

	interests, err := s.embedInterests(ctx, in.Interests)
	if err != nil {
		return Persona{}, err
	}
	for _, in := range interests {
		if _, err := tx.Exec(ctx, `
			INSERT INTO persona_interests (persona_id, topic, weight, embedding)
			VALUES ($1, $2, $3, $4)
		`, id, in.Topic, in.Weight, pgvector.NewVector(in.Embedding)); err != nil {
			return Persona{}, fmt.Errorf("persona: insert interest: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return Persona{}, fmt.Errorf("persona: commit: %w", err)
	}

	return Persona{
		ID:   id,
		Name: in.Name,
		Personality: Personality{
			Stance:     in.Stance,
			Interests:  interests,
			Skepticism: in.Skepticism,
			Verbosity:  verbosity,
		},
	}, nil
}

func (s *Store) embedInterests(ctx context.Context, in []InterestInput) ([]Interest, error) {
	if len(in) == 0 {
		return nil, nil
	}
	topics := make([]string, len(in))
	for i, it := range in {
		topics[i] = it.Topic
	}
	vecs, err := s.embedder.EmbedQueries(ctx, topics)
	if err != nil {
		return nil, fmt.Errorf("persona: embed interests: %w", err)
	}
	out := make([]Interest, len(in))
	for i, it := range in {
		out[i] = Interest{Topic: it.Topic, Weight: it.Weight, Embedding: vecs[i]}
	}
	return out, nil
}

func (s *Store) Get(ctx context.Context, id uuid.UUID) (Persona, error) {
	var p Persona
	var verbosity string
	p.ID = id
	if err := s.pool.QueryRow(ctx, `
		SELECT name, stance, verbosity, skepticism
		FROM personas WHERE id = $1
	`, id).Scan(&p.Name, &p.Personality.Stance, &verbosity, &p.Personality.Skepticism); err != nil {
		if err == pgx.ErrNoRows {
			return Persona{}, fmt.Errorf("persona: %w", ErrNotFound)
		}
		return Persona{}, fmt.Errorf("persona: get: %w", err)
	}
	p.Personality.Verbosity = Verbosity(verbosity)

	interests, err := s.interests(ctx, id)
	if err != nil {
		return Persona{}, err
	}
	p.Personality.Interests = interests
	return p, nil
}

func (s *Store) List(ctx context.Context) ([]Persona, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, name, stance, verbosity, skepticism FROM personas ORDER BY created_at
	`)
	if err != nil {
		return nil, fmt.Errorf("persona: list: %w", err)
	}
	defer rows.Close()

	var out []Persona
	for rows.Next() {
		var p Persona
		var verbosity string
		if err := rows.Scan(&p.ID, &p.Name, &p.Personality.Stance, &verbosity, &p.Personality.Skepticism); err != nil {
			return nil, fmt.Errorf("persona: scan: %w", err)
		}
		p.Personality.Verbosity = Verbosity(verbosity)
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("persona: iterate: %w", err)
	}

	// Interests are fetched per persona rather than joined: the list is small
	// (dev-scale personas) and this keeps the row-scanning code above simple.
	for i, p := range out {
		interests, err := s.interests(ctx, p.ID)
		if err != nil {
			return nil, err
		}
		out[i].Personality.Interests = interests
	}
	return out, nil
}

type UpdateInput struct {
	Stance     *string
	Verbosity  *Verbosity
	Skepticism *float32
}

func (s *Store) Update(ctx context.Context, id uuid.UUID, in UpdateInput) (Persona, error) {
	if in.Stance != nil {
		if _, err := s.pool.Exec(ctx,
			`UPDATE personas SET stance = $2, updated_at = now() WHERE id = $1`, id, *in.Stance,
		); err != nil {
			return Persona{}, fmt.Errorf("persona: update stance: %w", err)
		}
	}
	if in.Verbosity != nil {
		if _, err := s.pool.Exec(ctx,
			`UPDATE personas SET verbosity = $2, updated_at = now() WHERE id = $1`, id, string(*in.Verbosity),
		); err != nil {
			return Persona{}, fmt.Errorf("persona: update verbosity: %w", err)
		}
	}
	if in.Skepticism != nil {
		if _, err := s.pool.Exec(ctx,
			`UPDATE personas SET skepticism = $2, updated_at = now() WHERE id = $1`, id, *in.Skepticism,
		); err != nil {
			return Persona{}, fmt.Errorf("persona: update skepticism: %w", err)
		}
	}
	return s.Get(ctx, id)
}

func (s *Store) interests(ctx context.Context, personaID uuid.UUID) ([]Interest, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT topic, weight, embedding FROM persona_interests WHERE persona_id = $1
	`, personaID)
	if err != nil {
		return nil, fmt.Errorf("persona: interests: %w", err)
	}
	defer rows.Close()

	var out []Interest
	for rows.Next() {
		var in Interest
		var vec pgvector.Vector
		if err := rows.Scan(&in.Topic, &in.Weight, &vec); err != nil {
			return nil, fmt.Errorf("persona: scan interest: %w", err)
		}
		in.Embedding = vec.Slice()
		out = append(out, in)
	}
	return out, rows.Err()
}

// ErrNotFound is returned by Get when no persona has the given ID, so
// callers can map it to a 404 without matching on error text.
var ErrNotFound = fmt.Errorf("persona not found")
