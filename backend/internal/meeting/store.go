package meeting

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// Create inserts a meeting and its participant list in one transaction.
// Participants are ordered by their position in ps, which becomes
// speaking_order — see meeting_participants' schema comment.
func (s *Store) Create(ctx context.Context, topic string, ps []Participant) (uuid.UUID, error) {
	if len(ps) == 0 {
		return uuid.Nil, ErrNoParticipants
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return uuid.Nil, fmt.Errorf("meeting: begin create: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var id uuid.UUID
	if err := tx.QueryRow(ctx, `
		INSERT INTO meetings (topic) VALUES ($1) RETURNING id
	`, topic).Scan(&id); err != nil {
		return uuid.Nil, fmt.Errorf("meeting: insert meeting: %w", err)
	}

	for i, p := range ps {
		if _, err := tx.Exec(ctx, `
			INSERT INTO meeting_participants (meeting_id, persona_slug, persona_name, speaking_order)
			VALUES ($1, $2, $3, $4)
		`, id, p.Slug, p.Name, i); err != nil {
			return uuid.Nil, fmt.Errorf("meeting: insert participant %s: %w", p.Slug, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return uuid.Nil, fmt.Errorf("meeting: commit create: %w", err)
	}
	return id, nil
}

// Get loads a meeting's participants and full turn history (with
// citations), ready to render as GET /api/meetings/{id}.
func (s *Store) Get(ctx context.Context, id uuid.UUID) (Meeting, error) {
	m := Meeting{ID: id}
	if err := s.pool.QueryRow(ctx, `
		SELECT topic FROM meetings WHERE id = $1
	`, id).Scan(&m.Topic); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Meeting{}, ErrMeetingNotFound
		}
		return Meeting{}, fmt.Errorf("meeting: get: %w", err)
	}

	participants, err := s.participants(ctx, id)
	if err != nil {
		return Meeting{}, err
	}
	m.Participants = participants

	turns, err := s.Transcript(ctx, id)
	if err != nil {
		return Meeting{}, err
	}
	m.Turns = turns
	return m, nil
}

func (s *Store) participants(ctx context.Context, meetingID uuid.UUID) ([]Participant, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT persona_slug, persona_name, speaking_order
		FROM meeting_participants
		WHERE meeting_id = $1
		ORDER BY speaking_order
	`, meetingID)
	if err != nil {
		return nil, fmt.Errorf("meeting: participants: %w", err)
	}
	defer rows.Close()

	var out []Participant
	for rows.Next() {
		var p Participant
		if err := rows.Scan(&p.Slug, &p.Name, &p.SpeakingOrder); err != nil {
			return nil, fmt.Errorf("meeting: scan participant: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Transcript returns every turn so far, oldest first, citations included.
// The moderator calls this once per participant per round (not once per
// round) so each participant sees statements made earlier in its own round
// — see moderator.go's doc comment on why the participant loop must stay
// sequential for this to matter.
func (s *Store) Transcript(ctx context.Context, meetingID uuid.UUID) ([]Turn, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, seq, round, role, speaker_slug, speaker_name, content, created_at
		FROM turns
		WHERE meeting_id = $1
		ORDER BY seq
	`, meetingID)
	if err != nil {
		return nil, fmt.Errorf("meeting: transcript: %w", err)
	}
	defer rows.Close()

	var out []Turn
	for rows.Next() {
		var t Turn
		var speakerSlug *string
		if err := rows.Scan(&t.ID, &t.Seq, &t.Round, &t.Role, &speakerSlug, &t.SpeakerName, &t.Content, &t.CreatedAt); err != nil {
			return nil, fmt.Errorf("meeting: scan turn: %w", err)
		}
		if speakerSlug != nil {
			t.SpeakerSlug = *speakerSlug
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("meeting: iterate turns: %w", err)
	}

	byTurn, err := s.citationsByTurn(ctx, turnIDs(out))
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Citations = byTurn[out[i].ID]
	}
	return out, nil
}

func turnIDs(turns []Turn) []uuid.UUID {
	out := make([]uuid.UUID, len(turns))
	for i, t := range turns {
		out[i] = t.ID
	}
	return out
}

// AppendTurn assigns the next seq for the meeting and inserts the turn in
// one transaction, so concurrent appends cannot collide on seq. In practice
// a single meeting is only ever written by one moderator goroutine at a
// time (the round loop is sequential — see moderator.go), so the
// transaction is a safety net, not something concurrent callers are
// expected to race against.
func (s *Store) AppendTurn(ctx context.Context, meetingID uuid.UUID, t Turn) (Turn, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Turn{}, fmt.Errorf("meeting: begin append: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var nextSeq int
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(MAX(seq), -1) + 1 FROM turns WHERE meeting_id = $1
	`, meetingID).Scan(&nextSeq); err != nil {
		return Turn{}, fmt.Errorf("meeting: next seq: %w", err)
	}

	var speakerSlug any
	if t.SpeakerSlug != "" {
		speakerSlug = t.SpeakerSlug
	}

	t.Seq = nextSeq
	if err := tx.QueryRow(ctx, `
		INSERT INTO turns (meeting_id, seq, round, role, speaker_slug, speaker_name, content)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id, created_at
	`, meetingID, t.Seq, t.Round, t.Role, speakerSlug, t.SpeakerName, t.Content,
	).Scan(&t.ID, &t.CreatedAt); err != nil {
		return Turn{}, fmt.Errorf("meeting: insert turn: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return Turn{}, fmt.Errorf("meeting: commit append: %w", err)
	}
	return t, nil
}

// SaveCitations records the citations behind one turn, ranked in the order
// given. No foreign key ties chunk_id back to kaigi_knowledge — see
// turn_citations' schema comment — so this never fails due to a chunk not
// (yet, or any longer) existing there.
func (s *Store) SaveCitations(ctx context.Context, turnID uuid.UUID, cs []Citation) error {
	if len(cs) == 0 {
		return nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("meeting: begin citations: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for i, c := range cs {
		if _, err := tx.Exec(ctx, `
			INSERT INTO turn_citations
				(turn_id, chunk_id, document_id, rank, title, object_key, relevance_score, affinity_score)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		`, turnID, c.ChunkID, c.DocumentID, i, c.Title, c.ObjectKey, c.Relevance, c.Affinity); err != nil {
			return fmt.Errorf("meeting: insert citation: %w", err)
		}
	}
	return tx.Commit(ctx)
}

// citationsByTurn loads every citation for the given turns in one query
// (WHERE turn_id = ANY($1)) rather than one query per turn — Transcript used
// to call a per-turn citations() in a loop, which meant a 3-round meeting
// with 4 personas issued dozens of avoidable round trips per turn sent
// (moderator.runOneTurn calls Transcript fresh before every single
// participant's turn — see its own doc comment on why). Caught by code
// review, not by any test: the N+1 pattern is correct, just slow.
func (s *Store) citationsByTurn(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID][]Citation, error) {
	out := make(map[uuid.UUID][]Citation, len(ids))
	if len(ids) == 0 {
		return out, nil
	}

	rows, err := s.pool.Query(ctx, `
		SELECT turn_id, chunk_id, document_id, rank, title, object_key, relevance_score, affinity_score
		FROM turn_citations
		WHERE turn_id = ANY($1)
		ORDER BY turn_id, rank
	`, ids)
	if err != nil {
		return nil, fmt.Errorf("meeting: citations: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var turnID uuid.UUID
		var c Citation
		if err := rows.Scan(&turnID, &c.ChunkID, &c.DocumentID, &c.Rank, &c.Title, &c.ObjectKey, &c.Relevance, &c.Affinity); err != nil {
			return nil, fmt.Errorf("meeting: scan citation: %w", err)
		}
		out[turnID] = append(out[turnID], c)
	}
	return out, rows.Err()
}
