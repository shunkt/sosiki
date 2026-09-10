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

	"github.com/shun/kaigi/backend/internal/api"
	"github.com/shun/kaigi/backend/internal/config"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("server exited", "error", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg := config.Load()

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           api.NewHandler(cfg, log),
		ReadHeaderTimeout: 10 * time.Second,
		// ReadTimeout and WriteTimeout stay unset. Both are absolute deadlines on
		// the entire request/response, so either one would cut an SSE stream off
		// mid-conversation. The SSE handler sets per-frame write deadlines with
		// http.ResponseController instead.
		IdleTimeout: 60 * time.Second,
	}

	// Serve in the background so main can wait on a shutdown signal.
	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		log.Info("shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}
