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
	"github.com/shun/kaigi/backend/internal/chat"
	"github.com/shun/kaigi/backend/internal/config"
	"github.com/shun/kaigi/backend/internal/db"
	"github.com/shun/kaigi/backend/internal/objectstore"
	"github.com/shun/kaigi/backend/internal/persona"
	"github.com/shun/kaigi/backend/internal/retrieval"
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
	if err := cfg.Validate(); err != nil {
		// Fail at boot, not on the first chat request: a conversation API
		// with no LLM key configured is not a degraded service, it is a
		// misconfigured one.
		return err
	}

	ctx := context.Background()

	pool, err := db.New(ctx, cfg.DatabaseURL, log)
	if err != nil {
		return err
	}
	defer pool.Close()

	objects, err := objectstore.New(cfg.ObjectStore)
	if err != nil {
		return err
	}

	embedder := retrieval.NewEmbedder(cfg.EmbedURL)
	reranker := retrieval.NewReranker(cfg.RerankURL)
	searcher := retrieval.NewSearcher(pool, embedder, reranker, cfg.Retrieval)

	personas := persona.NewStore(pool, embedder)
	chatStore := chat.NewStore(pool)
	llm := chat.NewDeepSeekClient(cfg.LLM)
	engine := chat.NewEngine(llm, searcher, objects, chatStore, cfg)

	handler := api.NewHandler(cfg, api.Deps{
		Log:       log,
		Personas:  personas,
		Chat:      engine,
		ChatStore: chatStore,
		Objects:   objects,
	})

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           handler,
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
