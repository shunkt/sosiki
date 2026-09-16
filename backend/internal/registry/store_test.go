package registry

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shun/kaigi/backend/internal/db"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_REGISTRY_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("DATABASE_URL")
	}
	if dsn == "" {
		t.Skip("TEST_REGISTRY_DATABASE_URL/DATABASE_URL not set; skipping integration test")
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	pool, err := db.New(context.Background(), dsn, "registry", log)
	if err != nil {
		t.Fatalf("db.New: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func testCard(name string) *a2a.AgentCard {
	return &a2a.AgentCard{
		Name:    name,
		Version: "1.0.0",
		SupportedInterfaces: []*a2a.AgentInterface{
			a2a.NewAgentInterface("http://persona-critic:8082", a2a.TransportProtocolJSONRPC),
		},
	}
}

func TestUpsertAndGet(t *testing.T) {
	pool := testPool(t)
	store := NewStore(pool)
	ctx := context.Background()

	slug := "test-upsert-" + t.Name()
	personaID := uuid.New()

	if err := store.Upsert(ctx, Agent{
		Slug:      slug,
		BaseURL:   "http://persona-critic:8082",
		PersonaID: personaID,
		Card:      testCard("批評家"),
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	got, err := store.Get(ctx, slug)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.BaseURL != "http://persona-critic:8082" {
		t.Errorf("BaseURL = %q, want http://persona-critic:8082", got.BaseURL)
	}
	if got.PersonaID != personaID {
		t.Errorf("PersonaID = %v, want %v", got.PersonaID, personaID)
	}
	if got.Card == nil || got.Card.Name != "批評家" {
		t.Errorf("Card = %+v, want Name=批評家", got.Card)
	}
}

// TestUpsertIsIdempotentAndUpdatesLastSeenAt is the heartbeat contract: a
// second Upsert for the same slug does not create a duplicate row, and
// bumps last_seen_at — this is what Client.Run relies on to keep a
// registration alive with no separate Touch call.
func TestUpsertIsIdempotentAndUpdatesLastSeenAt(t *testing.T) {
	pool := testPool(t)
	store := NewStore(pool)
	ctx := context.Background()

	slug := "test-idempotent-" + t.Name()
	agent := Agent{Slug: slug, BaseURL: "http://persona-critic:8082", PersonaID: uuid.New(), Card: testCard("批評家")}

	if err := store.Upsert(ctx, agent); err != nil {
		t.Fatalf("first Upsert: %v", err)
	}
	first, err := store.Get(ctx, slug)
	if err != nil {
		t.Fatalf("Get after first Upsert: %v", err)
	}

	if err := store.Upsert(ctx, agent); err != nil {
		t.Fatalf("second Upsert: %v", err)
	}
	second, err := store.Get(ctx, slug)
	if err != nil {
		t.Fatalf("Get after second Upsert: %v", err)
	}

	if second.LastSeenAt.Before(first.LastSeenAt) {
		t.Errorf("second LastSeenAt (%v) is before first (%v)", second.LastSeenAt, first.LastSeenAt)
	}

	all, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	count := 0
	for _, a := range all {
		if a.Slug == slug {
			count++
		}
	}
	if count != 1 {
		t.Errorf("found %d rows for slug %q after two Upserts, want 1", count, slug)
	}
}

func TestGetNotFound(t *testing.T) {
	pool := testPool(t)
	store := NewStore(pool)

	if _, err := store.Get(context.Background(), "does-not-exist"); err != ErrNotFound {
		t.Errorf("Get error = %v, want ErrNotFound", err)
	}
}

func TestListOrdersBySlug(t *testing.T) {
	pool := testPool(t)
	store := NewStore(pool)
	ctx := context.Background()

	prefix := "test-list-order-" + t.Name() + "-"
	for _, slug := range []string{prefix + "zebra", prefix + "alpha"} {
		if err := store.Upsert(ctx, Agent{
			Slug: slug, BaseURL: "http://x:8082", PersonaID: uuid.New(), Card: testCard(slug),
		}); err != nil {
			t.Fatalf("Upsert(%s): %v", slug, err)
		}
	}

	all, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var seen []string
	for _, a := range all {
		if len(a.Slug) >= len(prefix) && a.Slug[:len(prefix)] == prefix {
			seen = append(seen, a.Slug)
		}
	}
	if len(seen) != 2 || seen[0] != prefix+"alpha" || seen[1] != prefix+"zebra" {
		t.Errorf("List order = %v, want [%salpha %szebra]", seen, prefix, prefix)
	}
}
