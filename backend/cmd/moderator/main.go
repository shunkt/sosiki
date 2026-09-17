// Command moderator is the frontend pod's backend half: it speaks plain
// REST+SSE to the browser and drives meetings by calling persona pods over
// A2A. It never talks to OpenAI directly and never opens kaigi_persona or
// kaigi_knowledge — see the plan's DB-per-function split.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/shun/kaigi/backend/internal/api"
	"github.com/shun/kaigi/backend/internal/config"
	"github.com/shun/kaigi/backend/internal/db"
	"github.com/shun/kaigi/backend/internal/meeting"
	"github.com/shun/kaigi/backend/internal/serve"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("moderator exited", "error", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	// cfg.Validate() is NOT called here: it requires OPENAI_API_KEY, which
	// belongs to the persona pod and seed — the moderator never calls
	// OpenAI. Its own requirements (a meeting database, a discovery URL) are
	// checked directly below instead.
	if cfg.Databases.Meeting == "" {
		return fmt.Errorf("moderator: no meeting database configured")
	}
	if cfg.Registry.URL == "" {
		return fmt.Errorf("moderator: REGISTRY_URL is required")
	}

	ctx := context.Background()

	pool, err := db.New(ctx, cfg.Databases.Meeting, "meeting", log)
	if err != nil {
		return err
	}
	defer pool.Close()

	meetings := meeting.NewStore(pool)
	discovery := api.NewDiscoveryClient(cfg.Registry.URL)
	dialer := meeting.NewA2ADialer(meeting.NewRegistryCardSource(cfg.Registry.URL))
	moderator := meeting.NewModerator(meetings, dialer, log, cfg.Meeting.MaxRounds)

	handler := api.NewHandler(cfg, api.Deps{
		Log:           log,
		Pool:          pool,
		Meetings:      meetings,
		Moderator:     moderator,
		Agents:        discovery,
		DefaultRounds: 1,
	})

	return serve.Run(ctx, log, cfg.Addr, handler)
}
