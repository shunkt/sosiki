// Command discovery serves the agent registry: persona pods register (and
// re-register) themselves here, and the moderator lists who's currently
// present. See internal/agentapi and internal/registry.
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/shun/kaigi/backend/internal/agentapi"
	"github.com/shun/kaigi/backend/internal/config"
	"github.com/shun/kaigi/backend/internal/db"
	"github.com/shun/kaigi/backend/internal/registry"
	"github.com/shun/kaigi/backend/internal/serve"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("discovery exited", "error", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	// discovery has no OpenAI key and no retrieval tuning to validate —
	// cfg.Validate() is for the persona pod and seed only. It does compute
	// agent presence from cfg.Registry.TTL/HeartbeatInterval at request
	// time, though, so cfg.ValidateRegistry() still applies here (not just
	// to cmd/persona, which self-registers on that same interval) — without
	// it, a misconfigured TTL boots successfully and silently serves wrong
	// presence data instead of failing fast.
	if err := cfg.ValidateRegistry(); err != nil {
		return err
	}

	ctx := context.Background()

	pool, err := db.New(ctx, cfg.Databases.Registry, "registry", log)
	if err != nil {
		return err
	}
	defer pool.Close()

	handler := agentapi.NewHandler(agentapi.Deps{
		Log:      log,
		Pool:     pool,
		Registry: registry.NewStore(pool),
		Resolver: agentapi.NewDefaultResolver(),
		TTL:      cfg.Registry.TTL,
	})

	return serve.Run(ctx, log, cfg.Addr, handler)
}
