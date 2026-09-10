package retrieval

import (
	"context"
	"fmt"
	"math"
	"sort"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	pgvector "github.com/pgvector/pgvector-go"

	"github.com/shun/kaigi/backend/internal/config"
	"github.com/shun/kaigi/backend/internal/persona"
)

// Candidate is one retrieved chunk carrying both the raw relevance score and
// the persona-specific affinity score, so callers can show how much the
// persona moved the ranking rather than just the blended result.
type Candidate struct {
	ChunkID    uuid.UUID
	DocumentID uuid.UUID
	Title      string
	Bucket     string
	ObjectKey  string
	Content    string
	ByteStart  int64
	ByteEnd    int64
	SizeBytes  int64
	Embedding  []float32

	Relevance float32 // cross-encoder score, roughly 0..1
	Affinity  float32 // persona interest match, -1..1
	Final     float32 // blended score used for ordering
}

// queryEmbedder and crossEncoder are narrow seams over Embedder and Reranker
// so tests can drive the blend/threshold logic without a live TEI server.
type queryEmbedder interface {
	EmbedQueries(ctx context.Context, texts []string) ([][]float32, error)
}

type crossEncoder interface {
	Rerank(ctx context.Context, query string, texts []string) ([]RerankResult, error)
}

type Searcher struct {
	pool     *pgxpool.Pool
	embedder queryEmbedder
	reranker crossEncoder
	cfg      config.RetrievalConfig
}

func NewSearcher(pool *pgxpool.Pool, embedder *Embedder, reranker *Reranker, cfg config.RetrievalConfig) *Searcher {
	return &Searcher{pool: pool, embedder: embedder, reranker: reranker, cfg: cfg}
}

// Search runs the persona-weighted retrieval pipeline for one or more
// rewritten queries and returns the top FinalN candidates, highest Final
// first. An empty result is a normal outcome (every candidate was cut by the
// skepticism threshold), not an error.
func (s *Searcher) Search(ctx context.Context, p persona.Persona, queries []string) ([]Candidate, error) {
	if len(queries) == 0 {
		return nil, nil
	}

	queryVecs, err := s.embedder.EmbedQueries(ctx, queries)
	if err != nil {
		return nil, fmt.Errorf("search: embed queries: %w", err)
	}

	byChunk := make(map[uuid.UUID]Candidate)
	bestDistance := make(map[uuid.UUID]float64)
	for _, qv := range queryVecs {
		rows, err := s.vectorSearch(ctx, qv)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if prev, ok := bestDistance[row.candidate.ChunkID]; !ok || row.distance < prev {
				bestDistance[row.candidate.ChunkID] = row.distance
				byChunk[row.candidate.ChunkID] = row.candidate
			}
		}
	}

	if len(byChunk) == 0 {
		return nil, nil
	}

	candidates := make([]Candidate, 0, len(byChunk))
	for _, c := range byChunk {
		candidates = append(candidates, c)
	}

	return rerankAndBlend(ctx, s.reranker, p, s.cfg, queries[0], candidates)
}

// rerankAndBlend applies the persona-weighted rerank to already-retrieved
// candidates. Split out from Search so the blend and threshold math — the
// core of what makes this persona-aware — can be unit tested with a fake
// crossEncoder and no live Postgres.
func rerankAndBlend(
	ctx context.Context, reranker crossEncoder, p persona.Persona, cfg config.RetrievalConfig,
	primaryQuery string, candidates []Candidate,
) ([]Candidate, error) {
	if len(candidates) == 0 {
		return nil, nil
	}

	// Rerank against the first (primary) rewritten query. Later queries only
	// widen recall; the persona's leading intent still drives ranking.
	texts := make([]string, len(candidates))
	for i, c := range candidates {
		texts[i] = c.Content
	}
	scores, err := reranker.Rerank(ctx, primaryQuery, texts)
	if err != nil {
		return nil, fmt.Errorf("search: rerank: %w", err)
	}
	for _, sc := range scores {
		c := candidates[sc.Index]
		c.Relevance = sc.Score
		candidates[sc.Index] = c
	}

	// A more skeptical persona demands stronger relevance before citing
	// anything — the threshold rises with Skepticism rather than the other
	// way around, so a credulous persona (0) still filters obvious noise.
	threshold := 0.15 + 0.45*p.Personality.Skepticism
	filtered := candidates[:0]
	for _, c := range candidates {
		if c.Relevance >= threshold {
			filtered = append(filtered, c)
		}
	}
	candidates = filtered
	if len(candidates) == 0 {
		return nil, nil
	}

	for i, c := range candidates {
		c.Affinity = affinity(c.Embedding, p.Personality.Interests)
		// Affinity is -1..1 and Relevance is roughly 0..1; blending them
		// unscaled would make PersonaInfluence's meaning depend on the
		// interest weights in play, defeating it as a single dial.
		affinity01 := (c.Affinity + 1) / 2
		lambda := float32(cfg.PersonaInfluence)
		c.Final = (1-lambda)*c.Relevance + lambda*affinity01
		candidates[i] = c
	}

	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Final > candidates[j].Final })

	if n := cfg.FinalN; n > 0 && len(candidates) > n {
		candidates = candidates[:n]
	}
	return candidates, nil
}

// affinity blends a chunk's alignment with each interest, weighted, then
// normalizes by the total weight magnitude so the result stays in -1..1
// regardless of how many interests a persona has or how they're split
// between positive and negative.
func affinity(chunkEmbedding []float32, interests []persona.Interest) float32 {
	if len(interests) == 0 {
		return 0
	}
	var weighted, totalWeight float64
	for _, in := range interests {
		w := float64(in.Weight)
		weighted += w * cosine(chunkEmbedding, in.Embedding)
		if w < 0 {
			totalWeight += -w
		} else {
			totalWeight += w
		}
	}
	if totalWeight == 0 {
		return 0
	}
	return float32(weighted / totalWeight)
}

func cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

type scoredRow struct {
	candidate Candidate
	distance  float64
}

// vectorSearch orders by <=> (cosine distance, smaller is closer) to match
// the chunks_embedding_hnsw index, which is built with vector_cosine_ops.
// Any other operator (<->, <#>) would silently skip the index.
func (s *Searcher) vectorSearch(ctx context.Context, queryVec []float32) ([]scoredRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT c.id, c.document_id, d.title, d.bucket, d.object_key, d.size_bytes,
		       c.content, c.byte_start, c.byte_end, c.embedding,
		       c.embedding <=> $1 AS distance
		FROM chunks c
		JOIN documents d ON d.id = c.document_id
		ORDER BY c.embedding <=> $1
		LIMIT $2
	`, pgvector.NewVector(queryVec), s.cfg.CandidateK)
	if err != nil {
		return nil, fmt.Errorf("search: vector query: %w", err)
	}
	defer rows.Close()

	var out []scoredRow
	for rows.Next() {
		var c Candidate
		var vec pgvector.Vector
		var distance float64
		if err := rows.Scan(
			&c.ChunkID, &c.DocumentID, &c.Title, &c.Bucket, &c.ObjectKey, &c.SizeBytes,
			&c.Content, &c.ByteStart, &c.ByteEnd, &vec, &distance,
		); err != nil {
			return nil, fmt.Errorf("search: scan row: %w", err)
		}
		c.Embedding = vec.Slice()
		out = append(out, scoredRow{candidate: c, distance: distance})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("search: iterate rows: %w", err)
	}
	return out, nil
}
