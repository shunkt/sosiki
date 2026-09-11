package db

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
)

// TestMigrateIsIdempotent runs the embedded migrations against a real
// Postgres twice and confirms the schema lands correctly and the second run
// applies nothing new. Gated by TEST_DATABASE_URL (or DATABASE_URL as a
// convenience against the docker-compose instance) since it needs pgvector.
func TestMigrateIsIdempotent(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("DATABASE_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL/DATABASE_URL not set; skipping integration test")
	}

	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	pool, err := New(ctx, dsn, log)
	if err != nil {
		t.Fatalf("New (first run): %v", err)
	}
	defer pool.Close()

	// Second run against the same pool must be a no-op, not an error — every
	// migration file's version already exists in schema_migrations.
	if err := Migrate(ctx, pool, log); err != nil {
		t.Fatalf("Migrate (second run) should be a no-op, got: %v", err)
	}

	var columnType string
	if err := pool.QueryRow(ctx, `
		SELECT format_type(atttypid, atttypmod) FROM pg_attribute
		WHERE attrelid = 'chunks'::regclass AND attname = 'embedding'
	`).Scan(&columnType); err != nil {
		t.Fatalf("check chunks.embedding type: %v", err)
	}
	if columnType != "vector(768)" {
		t.Errorf("chunks.embedding is %q, want vector(768)", columnType)
	}

	var indexExists bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM pg_indexes WHERE indexname = 'chunks_embedding_hnsw')
	`).Scan(&indexExists); err != nil {
		t.Fatalf("check hnsw index: %v", err)
	}
	if !indexExists {
		t.Error("chunks_embedding_hnsw index is missing")
	}
}
