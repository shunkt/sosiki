package retrieval

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	pgvector "github.com/pgvector/pgvector-go"

	"github.com/shun/kaigi/backend/internal/config"
	"github.com/shun/kaigi/backend/internal/persona"
)

// fakeReranker returns a fixed relevance score for every text, regardless of
// content, so tests control the input to the threshold/blend math precisely.
type fakeReranker struct {
	score float32
}

func (f fakeReranker) Rerank(_ context.Context, _ string, texts []string) ([]RerankResult, error) {
	out := make([]RerankResult, len(texts))
	for i := range texts {
		out[i] = RerankResult{Index: i, Score: f.score}
	}
	return out, nil
}

func candidateWithEmbedding(vec []float32) Candidate {
	return Candidate{ChunkID: uuid.New(), Content: "x", Embedding: vec}
}

func TestSkepticismRaisesThreshold(t *testing.T) {
	cfg := config.RetrievalConfig{FinalN: 100, PersonaInfluence: 0}
	// Score 0.4 clears a credulous persona's floor (0.15) but not a skeptical
	// one's (0.15 + 0.45*1.0 = 0.60).
	candidates := []Candidate{candidateWithEmbedding(nil)}

	credulous := persona.Persona{Personality: persona.Personality{Skepticism: 0}}
	got, err := rerankAndBlend(context.Background(), fakeReranker{score: 0.4}, credulous, cfg, "q", candidates)
	if err != nil {
		t.Fatalf("rerankAndBlend: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("credulous persona: got %d candidates, want 1 (threshold should pass)", len(got))
	}

	skeptical := persona.Persona{Personality: persona.Personality{Skepticism: 1}}
	got, err = rerankAndBlend(context.Background(), fakeReranker{score: 0.4}, skeptical, cfg, "q", candidates)
	if err != nil {
		t.Fatalf("rerankAndBlend: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("skeptical persona: got %d candidates, want 0 (threshold should cut)", len(got))
	}
}

func TestPersonaInfluenceBlend(t *testing.T) {
	// One interest perfectly aligned with the chunk (cosine = 1), so
	// affinity = 1 and its 0..1 rescale is 1 — a clean signal to check the
	// blend math against without depending on the cosine implementation.
	interest := persona.Interest{Topic: "t", Weight: 1, Embedding: []float32{1, 0}}
	p := persona.Persona{Personality: persona.Personality{Interests: []persona.Interest{interest}}}
	candidates := []Candidate{candidateWithEmbedding([]float32{1, 0})}

	tests := []struct {
		name   string
		lambda float64
		want   float32
	}{
		{"lambda=0 is pure relevance", 0, 0.7},
		{"lambda=1 is pure affinity", 1, 1.0},
		{"lambda=0.5 is the midpoint", 0.5, 0.85},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.RetrievalConfig{FinalN: 100, PersonaInfluence: tt.lambda}
			got, err := rerankAndBlend(context.Background(), fakeReranker{score: 0.7}, p, cfg, "q",
				[]Candidate{candidates[0]})
			if err != nil {
				t.Fatalf("rerankAndBlend: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("got %d candidates, want 1", len(got))
			}
			if diff := got[0].Final - tt.want; diff > 1e-4 || diff < -1e-4 {
				t.Errorf("Final = %v, want %v", got[0].Final, tt.want)
			}
		})
	}
}

func TestAffinityWithNoInterests(t *testing.T) {
	p := persona.Persona{} // no interests
	cfg := config.RetrievalConfig{FinalN: 100, PersonaInfluence: 0.5}
	candidates := []Candidate{candidateWithEmbedding([]float32{1, 0})}

	got, err := rerankAndBlend(context.Background(), fakeReranker{score: 0.7}, p, cfg, "q", candidates)
	if err != nil {
		t.Fatalf("rerankAndBlend: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d candidates, want 1", len(got))
	}
	if got[0].Affinity != 0 {
		t.Errorf("Affinity = %v, want 0 with no interests", got[0].Affinity)
	}
	// affinity01 = 0.5, so Final = 0.5*0.7 + 0.5*0.5 = 0.6
	if diff := got[0].Final - 0.6; diff > 1e-4 || diff < -1e-4 {
		t.Errorf("Final = %v, want 0.6", got[0].Final)
	}
}

func TestRerankAndBlendEmptyInput(t *testing.T) {
	cfg := config.RetrievalConfig{FinalN: 10}
	got, err := rerankAndBlend(context.Background(), fakeReranker{}, persona.Persona{}, cfg, "q", nil)
	if err != nil {
		t.Fatalf("rerankAndBlend: %v", err)
	}
	if got != nil {
		t.Errorf("got %v, want nil for empty candidates", got)
	}
}

// TestSearchAgainstPostgres is the acceptance test for this feature's core
// claim: two personas with different interests get different rankings for
// the identical query. It runs against the docker-compose stack (Postgres +
// live TEI embed/rerank) seeded by `go run ./cmd/seed`, and is skipped
// without TEST_DATABASE_URL/DATABASE_URL since it needs real data.
func TestSearchAgainstPostgres(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("DATABASE_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL/DATABASE_URL not set; skipping integration test")
	}
	embedURL := envOrSkip(t, "TEST_EMBED_URL", "http://localhost:8081")
	rerankURL := envOrSkip(t, "TEST_RERANK_URL", "http://localhost:8082")

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	embedder := NewEmbedder(embedURL)
	reranker := NewReranker(rerankURL)
	cfg := config.RetrievalConfig{CandidateK: 10, FinalN: 5, PersonaInfluence: 0.6}
	searcher := NewSearcher(pool, embedder, reranker, cfg)

	critic := loadSeededPersona(ctx, t, pool, "批評家")
	practitioner := loadSeededPersona(ctx, t, pool, "実務家")

	// Deliberately topic-neutral: neither persona's interest words appear in
	// it, so any ranking difference comes from the affinity blend, not from
	// the query itself favoring one persona's vocabulary.
	query := "合意形成アルゴリズムについて教えてください"

	criticResults, err := searcher.Search(ctx, critic, []string{query})
	if err != nil {
		t.Fatalf("Search (批評家): %v", err)
	}
	practitionerResults, err := searcher.Search(ctx, practitioner, []string{query})
	if err != nil {
		t.Fatalf("Search (実務家): %v", err)
	}
	if len(criticResults) == 0 || len(practitionerResults) == 0 {
		t.Fatal("expected results from both personas; is `go run ./cmd/seed` up to date?")
	}

	t.Logf("批評家: %s", titleOrder(criticResults))
	t.Logf("実務家: %s", titleOrder(practitionerResults))
	if titleOrder(criticResults) == titleOrder(practitionerResults) {
		t.Errorf("both personas produced the identical document ranking %v; "+
			"PersonaInfluence should have moved at least one persona's ordering",
			titleOrder(criticResults))
	}
}

func titleOrder(candidates []Candidate) string {
	titles := make([]string, len(candidates))
	for i, c := range candidates {
		titles[i] = c.Title
	}
	return strings.Join(titles, ",")
}

func loadSeededPersona(ctx context.Context, t *testing.T, pool *pgxpool.Pool, name string) persona.Persona {
	t.Helper()
	var p persona.Persona
	var verbosity string
	if err := pool.QueryRow(ctx, `
		SELECT id, name, stance, verbosity, skepticism FROM personas WHERE name = $1
	`, name).Scan(&p.ID, &p.Name, &p.Personality.Stance, &verbosity, &p.Personality.Skepticism); err != nil {
		t.Fatalf("load persona %q (run `go run ./cmd/seed` first): %v", name, err)
	}
	p.Personality.Verbosity = persona.Verbosity(verbosity)

	rows, err := pool.Query(ctx, `
		SELECT topic, weight, embedding FROM persona_interests WHERE persona_id = $1
	`, p.ID)
	if err != nil {
		t.Fatalf("load interests for %q: %v", name, err)
	}
	defer rows.Close()
	for rows.Next() {
		var in persona.Interest
		var vec pgvector.Vector
		if err := rows.Scan(&in.Topic, &in.Weight, &vec); err != nil {
			t.Fatalf("scan interest for %q: %v", name, err)
		}
		in.Embedding = vec.Slice()
		p.Personality.Interests = append(p.Personality.Interests, in)
	}
	return p
}

func envOrSkip(t *testing.T, key, fallback string) string {
	t.Helper()
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
