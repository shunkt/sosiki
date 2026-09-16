package persona

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

// fakeEmbedder returns a deterministic vector per topic so tests can assert
// on embedding width without a live OpenAI call.
type fakeEmbedder struct{ dims int }

func (f fakeEmbedder) EmbedQueries(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		v := make([]float32, f.dims)
		v[0] = float32(i)
		out[i] = v
	}
	return out, nil
}

// testPool opens (and migrates) the kaigi_persona database for integration
// tests, or skips if no DSN is configured — same convention as
// internal/db/db_test.go and internal/retrieval's integration tests.
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_PERSONA_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("DATABASE_URL")
	}
	if dsn == "" {
		t.Skip("TEST_PERSONA_DATABASE_URL/DATABASE_URL not set; skipping integration test")
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	pool, err := db.New(context.Background(), dsn, "persona", log)
	if err != nil {
		t.Fatalf("db.New: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// TestGetBySlugLoadsEmbeddings guards against the exact silent-failure GOTCHA
// the plan calls out: if GetBySlug (or the interests() helper it shares with
// Get) stops reading the embedding column, PersonaInfluence's affinity term
// goes to zero for every chunk with no error anywhere — retrieval still
// "works", it just quietly stops being persona-weighted.
func TestGetBySlugLoadsEmbeddings(t *testing.T) {
	pool := testPool(t)
	store := NewStore(pool, fakeEmbedder{dims: 1536})
	ctx := context.Background()

	slug := "test-critic-" + t.Name()
	created, err := store.Create(ctx, CreateInput{
		Slug:       slug,
		Name:       "テスト批評家",
		Stance:     "懐疑的",
		Skepticism: 0.8,
		Interests:  []InterestInput{{Topic: "形式手法", Weight: 0.7}},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() { deletePersona(t, pool, created.ID) })

	got, err := store.GetBySlug(ctx, slug)
	if err != nil {
		t.Fatalf("GetBySlug: %v", err)
	}
	if got.ID != created.ID {
		t.Errorf("ID = %v, want %v", got.ID, created.ID)
	}
	if len(got.Personality.Interests) != 1 {
		t.Fatalf("Interests = %v, want 1", got.Personality.Interests)
	}
	if len(got.Personality.Interests[0].Embedding) != 1536 {
		t.Errorf("Embedding length = %d, want 1536 (affinity silently degrades to 0 otherwise)",
			len(got.Personality.Interests[0].Embedding))
	}
}

// deletePersona removes a test-created persona (and, via ON DELETE CASCADE,
// its interests) so repeated test runs against a persistent database don't
// collide on personas.slug's UNIQUE constraint.
func deletePersona(t *testing.T, pool *pgxpool.Pool, id uuid.UUID) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `DELETE FROM personas WHERE id = $1`, id); err != nil {
		t.Logf("cleanup: delete persona %s: %v", id, err)
	}
}

func TestGetBySlugNotFound(t *testing.T) {
	pool := testPool(t)
	store := NewStore(pool, fakeEmbedder{dims: 1536})

	if _, err := store.GetBySlug(context.Background(), "does-not-exist"); err == nil {
		t.Error("GetBySlug for a missing slug returned nil error, want ErrNotFound")
	}
}

func TestListOrdersBySlug(t *testing.T) {
	pool := testPool(t)
	store := NewStore(pool, fakeEmbedder{dims: 1536})
	ctx := context.Background()

	prefix := "test-order-" + t.Name() + "-"
	for _, slug := range []string{prefix + "zebra", prefix + "alpha"} {
		created, err := store.Create(ctx, CreateInput{Slug: slug, Name: slug})
		if err != nil {
			t.Fatalf("Create(%s): %v", slug, err)
		}
		t.Cleanup(func() { deletePersona(t, pool, created.ID) })
	}

	all, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var seen []string
	for _, p := range all {
		if len(p.Slug) >= len(prefix) && p.Slug[:len(prefix)] == prefix {
			seen = append(seen, p.Slug)
		}
	}
	if len(seen) != 2 || seen[0] != prefix+"alpha" || seen[1] != prefix+"zebra" {
		t.Errorf("List order = %v, want [%salpha %szebra]", seen, prefix, prefix)
	}
}
