// Package db opens the Postgres pool and applies the embedded schema for one
// logical database at a time — see config.DatabaseConfig for how the four
// databases (meeting/registry/persona/knowledge) share a single Postgres
// instance.
package db

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"

	"github.com/jackc/pgx/v5/pgxpool"
)

// go:embed cannot reach above the package directory, which is why the SQL lives
// under internal/db rather than at the module root. It embeds the whole
// migrations tree — one subdirectory per logical database ("set") — not a
// flat migrations/*.sql: each service only ever applies its own set.
//
//go:embed migrations
var migrationFS embed.FS

// New opens the pool for one logical database and brings its schema up to
// date. Migrating at startup keeps development to a single command; there is
// no separate migrate binary. set must be one of the migrations/ subdirectory
// names (config.DBMeeting etc, minus the "kaigi_" prefix — see Migrate).
func New(ctx context.Context, databaseURL, set string, log *slog.Logger) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("db: open pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("db: ping: %w", err)
	}
	if err := Migrate(ctx, pool, set, log); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

// migrationLockKey is an arbitrary, fixed pg_advisory_lock key. Advisory
// locks are scoped to the database they're taken in, so one constant is
// enough — kaigi_persona and kaigi_knowledge each get their own lock,
// unrelated to kaigi_meeting's or kaigi_registry's.
const migrationLockKey = 847264

// Migrate applies every embedded migration in the given set that has not run
// yet, in filename order. It is safe to call on every boot — including
// concurrently from multiple processes against the same database, which is
// the normal case for kaigi_persona and kaigi_knowledge (every persona pod
// migrates both on its own boot, and pods start at roughly the same time in
// compose/k8s). A session-level pg_advisory_lock serializes those
// concurrent Migrate calls: without it, two pods racing on the same
// `CREATE EXTENSION IF NOT EXISTS` in migration 0001 can both pass the
// existence check and then collide inserting into pg_extension, since
// IF NOT EXISTS is not safe against a concurrent DDL race on its own — this
// was caught by an actual `docker compose up` with two persona pods
// starting together, not by any single-process test.
//
// schema_migrations lives in the target database itself, so each of the
// four databases tracks its own applied versions independently — no set
// prefix is needed in the version column.
func Migrate(ctx context.Context, pool *pgxpool.Pool, set string, log *slog.Logger) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("db: acquire connection for migration lock: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrationLockKey); err != nil {
		return fmt.Errorf("db: acquire migration lock: %w", err)
	}
	defer func() {
		if _, err := conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, migrationLockKey); err != nil && log != nil {
			log.Warn("release migration lock failed", "set", set, "error", err)
		}
	}()

	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    text PRIMARY KEY,
			applied_at timestamptz NOT NULL DEFAULT now()
		)`); err != nil {
		return fmt.Errorf("db: create schema_migrations: %w", err)
	}

	names, err := migrationNames(set)
	if err != nil {
		return err
	}

	for _, name := range names {
		var exists bool
		if err := pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)`, name,
		).Scan(&exists); err != nil {
			return fmt.Errorf("db: check migration %s: %w", name, err)
		}
		if exists {
			continue
		}

		body, err := migrationFS.ReadFile("migrations/" + set + "/" + name)
		if err != nil {
			return fmt.Errorf("db: read migration %s/%s: %w", set, name, err)
		}

		// One transaction per migration: a failure leaves the database on the
		// last good version instead of half-applied.
		tx, err := pool.Begin(ctx)
		if err != nil {
			return fmt.Errorf("db: begin %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, string(body)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("db: apply %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO schema_migrations (version) VALUES ($1)`, name,
		); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("db: record %s: %w", name, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("db: commit %s: %w", name, err)
		}

		if log != nil {
			log.Info("migration applied", "set", set, "version", name)
		}
	}
	return nil
}

func migrationNames(set string) ([]string, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations/"+set)
	if err != nil {
		return nil, fmt.Errorf("db: unknown migration set %q: %w", set, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}
