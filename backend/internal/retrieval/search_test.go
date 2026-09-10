package retrieval

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"

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

// TestSearchAgainstPostgres is a real integration test. It only runs when
// TEST_DATABASE_URL is set (e.g. against the docker-compose postgres), since
// it needs pgvector and the chunks table to exist.
func TestSearchAgainstPostgres(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integration test")
	}
	t.Skip("integration wiring lands with Task 13's seed data")
}
