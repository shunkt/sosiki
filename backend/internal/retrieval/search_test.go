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

// candidateWithRelevance builds a candidate the way Search does now: the
// relevance score is the cosine similarity already attached by the vector
// query, so blend takes it as given rather than computing it.
func candidateWithRelevance(relevance float32, vec []float32) Candidate {
	return Candidate{ChunkID: uuid.New(), Content: "x", Embedding: vec, Relevance: relevance}
}

func blendConfig(finalN int, lambda float64) config.RetrievalConfig {
	return config.RetrievalConfig{
		FinalN:           finalN,
		PersonaInfluence: lambda,
		RelevanceFloor:   0.25,
		SkepticismSpan:   0.20,
	}
}

func TestSkepticismRaisesThreshold(t *testing.T) {
	cfg := blendConfig(100, 0)
	// Score 0.30 clears a credulous persona's floor (0.25) but not a
	// skeptical one's (0.25 + 0.20*1.0 = 0.45).
	credulous := persona.Persona{Personality: persona.Personality{Skepticism: 0}}
	got := blend(credulous, cfg, []Candidate{candidateWithRelevance(0.30, nil)})
	if len(got) != 1 {
		t.Errorf("credulous persona: got %d candidates, want 1 (threshold should pass)", len(got))
	}

	skeptical := persona.Persona{Personality: persona.Personality{Skepticism: 1}}
	got = blend(skeptical, cfg, []Candidate{candidateWithRelevance(0.30, nil)})
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
			cfg := blendConfig(100, tt.lambda)
			got := blend(p, cfg, []Candidate{candidateWithRelevance(0.7, []float32{1, 0})})
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
	cfg := blendConfig(100, 0.5)

	got := blend(p, cfg, []Candidate{candidateWithRelevance(0.7, []float32{1, 0})})
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

func TestBlendEmptyInput(t *testing.T) {
	cfg := blendConfig(10, 0)
	got := blend(persona.Persona{}, cfg, nil)
	if got != nil {
		t.Errorf("got %v, want nil for empty candidates", got)
	}
}

// TestSearchAgainstPostgres is the acceptance test for this feature's core
// claim: two personas with different interests get different rankings for
// the identical query. It runs against Postgres seeded by `go run
// ./cmd/seed`, using the live OpenAI embeddings API, and is skipped without
// OPENAI_API_KEY/TEST_DATABASE_URL/DATABASE_URL since it needs both.
func TestSearchAgainstPostgres(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("DATABASE_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL/DATABASE_URL not set; skipping integration test")
	}
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		t.Skip("OPENAI_API_KEY not set; skipping integration test")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	embedder := NewEmbedder(config.EmbedConfig{
		BaseURL: "https://api.openai.com/v1",
		APIKey:  apiKey,
		Model:   "text-embedding-3-small",
	})
	cfg := config.RetrievalConfig{
		CandidateK: 10, FinalN: 5, PersonaInfluence: 0.6,
		RelevanceFloor: 0.25, SkepticismSpan: 0.20,
	}
	searcher := NewSearcher(pool, embedder, cfg)

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
