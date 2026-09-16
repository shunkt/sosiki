package db

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
)

// TestMigrateIsIdempotent runs the knowledge set's embedded migrations
// against a real Postgres twice and confirms the schema lands correctly and
// the second run applies nothing new. Gated by TEST_KNOWLEDGE_DATABASE_URL
// (falling back to TEST_POSTGRES_BASE_URL+"/kaigi_knowledge", then
// DATABASE_URL as a convenience against an older single-database setup)
// since it needs pgvector.
func TestMigrateIsIdempotent(t *testing.T) {
	dsn := os.Getenv("TEST_KNOWLEDGE_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("DATABASE_URL")
	}
	if dsn == "" {
		t.Skip("TEST_KNOWLEDGE_DATABASE_URL/DATABASE_URL not set; skipping integration test")
	}

	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	pool, err := New(ctx, dsn, "knowledge", log)
	if err != nil {
		t.Fatalf("New (first run): %v", err)
	}
	defer pool.Close()

	// Second run against the same pool must be a no-op, not an error — every
	// migration file's version already exists in schema_migrations.
	if err := Migrate(ctx, pool, "knowledge", log); err != nil {
		t.Fatalf("Migrate (second run) should be a no-op, got: %v", err)
	}

	var columnType string
	if err := pool.QueryRow(ctx, `
		SELECT format_type(atttypid, atttypmod) FROM pg_attribute
		WHERE attrelid = 'chunks'::regclass AND attname = 'embedding'
	`).Scan(&columnType); err != nil {
		t.Fatalf("check chunks.embedding type: %v", err)
	}
	// 1536 is text-embedding-3-small's width.
	if columnType != "vector(1536)" {
		t.Errorf("chunks.embedding is %q, want vector(1536)", columnType)
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

// TestMigrateUnknownSet confirms an unknown migration set fails loudly at
// Migrate time rather than silently applying zero migrations — the failure
// mode go:embed's directory-tree change (Task 2's GOTCHA) makes possible if
// migrationNames ever regresses to reading an empty or wrong directory.
func TestMigrateUnknownSet(t *testing.T) {
	if _, err := migrationNames("nope"); err == nil {
		t.Error("migrationNames(\"nope\") error = nil, want error for unknown set")
	}
}

// TestAllSetsHaveMigrations is a fast, non-integration guard against the
// go:embed pattern regressing to "migrations/*.sql" (which would silently
// embed zero files from any subdirectory — see db.go's GOTCHA) by asserting
// each known set actually has at least one migration file embedded.
func TestAllSetsHaveMigrations(t *testing.T) {
	for _, set := range []string{"meeting", "registry", "persona", "knowledge"} {
		names, err := migrationNames(set)
		if err != nil {
			t.Errorf("migrationNames(%q): %v", set, err)
			continue
		}
		if len(names) == 0 {
			t.Errorf("migrationNames(%q) = empty, want at least one migration file embedded", set)
		}
	}
}
