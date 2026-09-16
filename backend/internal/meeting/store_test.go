package meeting

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shun/kaigi/backend/internal/db"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_MEETING_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("DATABASE_URL")
	}
	if dsn == "" {
		t.Skip("TEST_MEETING_DATABASE_URL/DATABASE_URL not set; skipping integration test")
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	pool, err := db.New(context.Background(), dsn, "meeting", log)
	if err != nil {
		t.Fatalf("db.New: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func testParticipants() []Participant {
	return []Participant{
		{Slug: "critic", Name: "批評家", BaseURL: "http://persona-critic:8082"},
		{Slug: "pragmatist", Name: "実務家", BaseURL: "http://persona-pragmatist:8082"},
	}
}

func TestCreateRejectsNoParticipants(t *testing.T) {
	pool := testPool(t)
	store := NewStore(pool)

	if _, err := store.Create(context.Background(), "topic", nil); err != ErrNoParticipants {
		t.Errorf("Create error = %v, want ErrNoParticipants", err)
	}
}

func TestCreateAndGetPreservesSpeakingOrder(t *testing.T) {
	pool := testPool(t)
	store := NewStore(pool)
	ctx := context.Background()

	id, err := store.Create(ctx, "分散合意について", testParticipants())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	m, err := store.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if m.Topic != "分散合意について" {
		t.Errorf("Topic = %q, want 分散合意について", m.Topic)
	}
	if len(m.Participants) != 2 {
		t.Fatalf("Participants = %v, want 2", m.Participants)
	}
	if m.Participants[0].Slug != "critic" || m.Participants[1].Slug != "pragmatist" {
		t.Errorf("Participants order = %v, want [critic pragmatist]", m.Participants)
	}
}

func TestGetNotFound(t *testing.T) {
	pool := testPool(t)
	store := NewStore(pool)

	if _, err := store.Get(context.Background(), uuid.New()); err != ErrMeetingNotFound {
		t.Errorf("Get error = %v, want ErrMeetingNotFound", err)
	}
}

// TestAppendTurnAssignsSequentialSeq is the seq-numbering contract
// moderator.go depends on: three appends in a row land as 0, 1, 2 with no
// gaps, and Transcript returns them in that order.
func TestAppendTurnAssignsSequentialSeq(t *testing.T) {
	pool := testPool(t)
	store := NewStore(pool)
	ctx := context.Background()

	id, err := store.Create(ctx, "topic", testParticipants())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	turns := []Turn{
		{Round: 0, Role: "user", SpeakerName: "user", Content: "議題です"},
		{Round: 1, Role: "persona", SpeakerSlug: "critic", SpeakerName: "批評家", Content: "批評家の発言"},
		{Round: 1, Role: "persona", SpeakerSlug: "pragmatist", SpeakerName: "実務家", Content: "実務家の発言"},
	}
	for i, in := range turns {
		got, err := store.AppendTurn(ctx, id, in)
		if err != nil {
			t.Fatalf("AppendTurn[%d]: %v", i, err)
		}
		if got.Seq != i {
			t.Errorf("AppendTurn[%d].Seq = %d, want %d", i, got.Seq, i)
		}
	}

	transcript, err := store.Transcript(ctx, id)
	if err != nil {
		t.Fatalf("Transcript: %v", err)
	}
	if len(transcript) != 3 {
		t.Fatalf("Transcript len = %d, want 3", len(transcript))
	}
	for i, turn := range transcript {
		if turn.Seq != i {
			t.Errorf("Transcript[%d].Seq = %d, want %d", i, turn.Seq, i)
		}
	}
	if transcript[0].SpeakerSlug != "" {
		t.Errorf("human turn SpeakerSlug = %q, want empty", transcript[0].SpeakerSlug)
	}
	if transcript[1].SpeakerSlug != "critic" {
		t.Errorf("second turn SpeakerSlug = %q, want critic", transcript[1].SpeakerSlug)
	}
}

func TestSaveAndLoadCitations(t *testing.T) {
	pool := testPool(t)
	store := NewStore(pool)
	ctx := context.Background()

	id, err := store.Create(ctx, "topic", testParticipants())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	turn, err := store.AppendTurn(ctx, id, Turn{
		Round: 1, Role: "persona", SpeakerSlug: "critic", SpeakerName: "批評家", Content: "発言",
	})
	if err != nil {
		t.Fatalf("AppendTurn: %v", err)
	}

	cs := []Citation{
		{ChunkID: uuid.New(), DocumentID: uuid.New(), Title: "raft-paper.md", Relevance: 0.9, Affinity: 0.3},
		{ChunkID: uuid.New(), DocumentID: uuid.New(), Title: "paxos-made-simple.md", Relevance: 0.8, Affinity: -0.1},
	}
	if err := store.SaveCitations(ctx, turn.ID, cs); err != nil {
		t.Fatalf("SaveCitations: %v", err)
	}

	transcript, err := store.Transcript(ctx, id)
	if err != nil {
		t.Fatalf("Transcript: %v", err)
	}
	if len(transcript) != 1 || len(transcript[0].Citations) != 2 {
		t.Fatalf("Transcript = %+v, want 1 turn with 2 citations", transcript)
	}
	if transcript[0].Citations[0].Title != "raft-paper.md" {
		t.Errorf("Citations[0].Title = %q, want raft-paper.md (rank order)", transcript[0].Citations[0].Title)
	}
}
