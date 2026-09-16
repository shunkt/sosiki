// Command moderator is the frontend pod's backend half: it speaks plain
// REST+SSE to the browser and drives meetings by calling persona pods over
// A2A. It never talks to OpenAI directly and never opens kaigi_persona or
// kaigi_knowledge — see the plan's DB-per-function split.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/shun/kaigi/backend/internal/api"
	"github.com/shun/kaigi/backend/internal/config"
	"github.com/shun/kaigi/backend/internal/db"
	"github.com/shun/kaigi/backend/internal/meeting"
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

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		// ReadTimeout and WriteTimeout stay unset. Both are absolute
		// deadlines on the entire request/response, so either one would cut
		// an SSE stream off mid-meeting. The SSE handler sets per-frame
		// write deadlines with http.ResponseController instead.
		IdleTimeout: 60 * time.Second,
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
