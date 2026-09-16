// Command discovery serves the agent registry: persona pods register (and
// re-register) themselves here, and the moderator lists who's currently
// present. See internal/agentapi and internal/registry.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/shun/kaigi/backend/internal/agentapi"
	"github.com/shun/kaigi/backend/internal/config"
	"github.com/shun/kaigi/backend/internal/db"
	"github.com/shun/kaigi/backend/internal/registry"
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
	// cfg.Validate() is for the persona pod and seed only. Its own
	// requirement is just a database to connect to, checked by db.New below.

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

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	stopCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	select {
	case err := <-errCh:
		return err
	case <-stopCtx.Done():
		log.Info("shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}
