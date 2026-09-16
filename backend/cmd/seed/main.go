// Command seed loads backend/testdata/knowledge/*.md into MinIO and
// kaigi_knowledge, and creates two sample personas with contrasting
// personalities into kaigi_persona so the persona-weighted rerank has
// something visible to demonstrate. It is idempotent: re-running it does
// not duplicate documents, chunks, or personas.
//
// Persona slugs seeded here ("critic", "pragmatist") are a contract with the
// persona pod manifests: each cmd/persona process is configured with
// PERSONA_SLUG and refuses to start if no row with that slug exists — see
// persona.Store.GetBySlug.
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	pgvector "github.com/pgvector/pgvector-go"

	"github.com/shun/kaigi/backend/internal/config"
	"github.com/shun/kaigi/backend/internal/db"
	"github.com/shun/kaigi/backend/internal/persona"
	"github.com/shun/kaigi/backend/internal/retrieval"
)

const (
	// chunkSize and chunkOverlap are in runes, not bytes — Japanese text
	// makes multi-byte runes the common case, and chunking by byte count
	// would slice a rune in half at arbitrary boundaries.
	chunkSize    = 800
	chunkOverlap = 150
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	if err := run(context.Background(), log); err != nil {
		log.Error("seed failed", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	// seed calls OpenAI directly (embeddings) and creates personas whose
	// downstream persona pods need OPENAI_API_KEY too, so this is a boot
	// error here just as it is for cmd/persona.
	if err := cfg.Validate(); err != nil {
		return err
	}

	knowledgePool, err := db.New(ctx, cfg.Databases.Knowledge, "knowledge", log)
	if err != nil {
		return err
	}
	defer knowledgePool.Close()

	personaPool, err := db.New(ctx, cfg.Databases.Persona, "persona", log)
	if err != nil {
		return err
	}
	defer personaPool.Close()

	minioClient, err := minio.New(cfg.ObjectStore.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.ObjectStore.AccessKey, cfg.ObjectStore.SecretKey, ""),
		Secure: cfg.ObjectStore.UseSSL,
	})
	if err != nil {
		return fmt.Errorf("seed: new minio client: %w", err)
	}

	embedder := retrieval.NewEmbedder(cfg.Embed)
	personas := persona.NewStore(personaPool, embedder)

	if err := seedDocuments(ctx, log, knowledgePool, minioClient, embedder, cfg.ObjectStore.Bucket); err != nil {
		return err
	}
	if err := seedPersonas(ctx, log, personaPool, personas); err != nil {
		return err
	}

	log.Info("seed complete")
	return nil
}

func seedDocuments(ctx context.Context, log *slog.Logger, pool *pgxpool.Pool, mc *minio.Client, embedder *retrieval.Embedder, bucket string) error {
	dir := filepath.Join("testdata", "knowledge")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("seed: read %s: %w", dir, err)
	}

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		path := filepath.Join(dir, e.Name())
		if err := seedOneDocument(ctx, log, pool, mc, embedder, bucket, e.Name(), path); err != nil {
			return fmt.Errorf("seed: %s: %w", e.Name(), err)
		}
	}
	return nil
}

func seedOneDocument(ctx context.Context, log *slog.Logger, pool *pgxpool.Pool, mc *minio.Client, embedder *retrieval.Embedder, bucket, name, path string) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read file: %w", err)
	}

	objectKey := "knowledge/" + name
	if _, err := mc.PutObject(ctx, bucket, objectKey, bytes.NewReader(content), int64(len(content)),
		minio.PutObjectOptions{ContentType: "text/markdown"},
	); err != nil {
		return fmt.Errorf("upload to minio: %w", err)
	}

	var documentID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO documents (title, bucket, object_key, content_type, size_bytes)
		VALUES ($1, $2, $3, 'text/markdown', $4)
		ON CONFLICT (bucket, object_key) DO UPDATE
			SET title = EXCLUDED.title, size_bytes = EXCLUDED.size_bytes
		RETURNING id
	`, name, bucket, objectKey, len(content)).Scan(&documentID); err != nil {
		return fmt.Errorf("upsert document: %w", err)
	}

	chunks := chunkText(string(content))
	texts := make([]string, len(chunks))
	for i, c := range chunks {
		texts[i] = c.text
	}
	vectors, err := embedder.EmbedDocuments(ctx, texts)
	if err != nil {
		return fmt.Errorf("embed chunks: %w", err)
	}

	for i, c := range chunks {
		if _, err := pool.Exec(ctx, `
			INSERT INTO chunks (document_id, chunk_index, content, byte_start, byte_end, embedding)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (document_id, chunk_index) DO UPDATE
				SET content = EXCLUDED.content, byte_start = EXCLUDED.byte_start,
				    byte_end = EXCLUDED.byte_end, embedding = EXCLUDED.embedding
		`, documentID, i, c.text, c.byteStart, c.byteEnd, pgvector.NewVector(vectors[i])); err != nil {
			return fmt.Errorf("upsert chunk %d: %w", i, err)
		}
	}

	log.Info("seeded document", "name", name, "chunks", len(chunks))
	return nil
}

type chunk struct {
	text      string
	byteStart int
	byteEnd   int
}

// chunkText splits s into overlapping windows measured in runes, but records
// byte offsets — chunks.byte_start/byte_end must be byte positions to line up
// with objectstore.Store.ExpandContext's Range GET against the raw object.
func chunkText(s string) []chunk {
	runes := []rune(s)
	if len(runes) == 0 {
		return nil
	}

	// byteOffsetOf finds the byte offset in s of the rune at index runeIdx by
	// re-encoding the prefix. Simpler than maintaining a running byte
	// position through the loop below and just as correct for this size of
	// seed data.
	byteOffsetOf := func(runeIdx int) int {
		if runeIdx >= len(runes) {
			return len(s)
		}
		return len(string(runes[:runeIdx]))
	}

	var chunks []chunk
	step := chunkSize - chunkOverlap
	for start := 0; start < len(runes); start += step {
		end := min(start+chunkSize, len(runes))
		text := string(runes[start:end])
		chunks = append(chunks, chunk{
			text:      text,
			byteStart: byteOffsetOf(start),
			byteEnd:   byteOffsetOf(end),
		})
		if end == len(runes) {
			break
		}
	}
	return chunks
}

func seedPersonas(ctx context.Context, log *slog.Logger, pool *pgxpool.Pool, personas *persona.Store) error {
	specs := []persona.CreateInput{
		{
			Slug:       "critic",
			Name:       "批評家",
			Stance:     "主張は額面通り受け取らず、根拠の強さと反証可能性を重視する",
			Verbosity:  persona.VerbosityDetailed,
			Skepticism: 0.85,
			Interests: []persona.InterestInput{
				{Topic: "形式的検証", Weight: 0.8},
				{Topic: "マーケティング上の宣伝文句", Weight: -0.5},
			},
		},
		{
			Slug:       "pragmatist",
			Name:       "実務家",
			Stance:     "理論的な優劣よりも、運用現場でどれだけ手間が減るかを重視する",
			Verbosity:  persona.VerbosityConcise,
			Skepticism: 0.3,
			Interests: []persona.InterestInput{
				{Topic: "運用コスト", Weight: 0.9},
				{Topic: "理論的な純粋性", Weight: -0.3},
			},
		},
	}

	for _, spec := range specs {
		_, err := personas.GetBySlug(ctx, spec.Slug)
		switch {
		case errors.Is(err, persona.ErrNotFound):
			if _, err := personas.Create(ctx, spec); err != nil {
				return fmt.Errorf("create persona %s: %w", spec.Slug, err)
			}
			log.Info("seeded persona", "slug", spec.Slug, "name", spec.Name)
		case err != nil:
			return fmt.Errorf("check persona %s: %w", spec.Slug, err)
		default:
			log.Info("persona already seeded, skipping", "slug", spec.Slug)
		}
	}
	return nil
}
