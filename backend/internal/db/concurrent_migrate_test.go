package db

import (
	"context"
	"io"
	"log/slog"
	"os"
	"sync"
	"testing"
)

// TestConcurrentMigrateDoesNotRace reproduces the exact scenario an actual
// `docker compose up` hit: multiple persona pods each call db.New (which
// calls Migrate) against the SAME shared database at roughly the same time.
// Before the pg_advisory_lock fix, two callers could both pass 0001_init's
// "not yet applied" check and race on CREATE EXTENSION IF NOT EXISTS,
// failing with a duplicate-key error from pg_extension. Gated the same way
// as the other db integration tests.
func TestConcurrentMigrateDoesNotRace(t *testing.T) {
	dsn := os.Getenv("TEST_PERSONA_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("DATABASE_URL")
	}
	if dsn == "" {
		t.Skip("TEST_PERSONA_DATABASE_URL/DATABASE_URL not set; skipping integration test")
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()

	const concurrency = 5
	var wg sync.WaitGroup
	errs := make([]error, concurrency)
	for i := range concurrency {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			pool, err := New(ctx, dsn, "persona", log)
			if err != nil {
				errs[i] = err
				return
			}
			pool.Close()
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("concurrent New/Migrate [%d]: %v", i, err)
		}
	}
}
