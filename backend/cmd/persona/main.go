// Command persona is one persona's A2A server: it serves an agent card at
// /.well-known/agent-card.json, answers JSON-RPC (SendMessage /
// SendStreamingMessage) at /, and registers itself with the discovery pod on
// a heartbeat. Exactly one persona per process — see PERSONA_SLUG.
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

	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/shun/kaigi/backend/internal/a2aconv"
	"github.com/shun/kaigi/backend/internal/chat"
	"github.com/shun/kaigi/backend/internal/config"
	"github.com/shun/kaigi/backend/internal/db"
	"github.com/shun/kaigi/backend/internal/objectstore"
	"github.com/shun/kaigi/backend/internal/persona"
	"github.com/shun/kaigi/backend/internal/personaexec"
	"github.com/shun/kaigi/backend/internal/registry"
	"github.com/shun/kaigi/backend/internal/retrieval"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("persona exited", "error", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	if cfg.PersonaSlug == "" {
		return fmt.Errorf("persona: PERSONA_SLUG is required")
	}
	if cfg.A2A.PublicURL == "" {
		return fmt.Errorf("persona: A2A_PUBLIC_URL is required")
	}
	if err := cfg.ValidateRegistry(); err != nil {
		return err
	}

	ctx := context.Background()

	// Two pools: personaPool for this persona's own row and interests,
	// knowledgePool for the chunks it searches — see the plan's DB-per-
	// function split. A persona pod never opens kaigi_meeting or
	// kaigi_registry.
	personaPool, err := db.New(ctx, cfg.Databases.Persona, "persona", log)
	if err != nil {
		return err
	}
	defer personaPool.Close()

	knowledgePool, err := db.New(ctx, cfg.Databases.Knowledge, "knowledge", log)
	if err != nil {
		return err
	}
	defer knowledgePool.Close()

	objects, err := objectstore.New(cfg.ObjectStore)
	if err != nil {
		return err
	}

	embedder := retrieval.NewEmbedder(cfg.Embed)
	searcher := retrieval.NewSearcher(knowledgePool, embedder, cfg.Retrieval)

	personas := persona.NewStore(personaPool, embedder)
	p, err := personas.GetBySlug(ctx, cfg.PersonaSlug)
	if err != nil {
		if errors.Is(err, persona.ErrNotFound) {
			return fmt.Errorf("persona: %q not found in kaigi_persona; run the seed job first", cfg.PersonaSlug)
		}
		return fmt.Errorf("persona: load %q: %w", cfg.PersonaSlug, err)
	}

	llm := chat.NewOpenAIClient(cfg.LLM)
	engine := chat.NewEngine(llm, searcher, objects, cfg)
	exec := personaexec.New(engine, p, log)

	handler := a2asrv.NewHandler(exec)
	card := a2aconv.PersonaCard(p, cfg.A2A.PublicURL)

	mux := http.NewServeMux()
	mux.Handle(a2asrv.WellKnownAgentCardPath, a2asrv.NewStaticAgentCardHandler(card))
	mux.HandleFunc("GET /healthz", healthz(personaPool, knowledgePool))
	mux.Handle("/", a2asrv.NewJSONRPCHandler(handler))

	// Register with discovery and keep re-registering on a heartbeat, for
	// as long as the process runs. Registration failures are logged and
	// retried, never fatal — see registry.Client.Run's doc comment.
	regClient := &registry.Client{
		HTTP:         &http.Client{Timeout: 10 * time.Second},
		DiscoveryURL: cfg.Registry.URL,
		SelfURL:      cfg.A2A.PublicURL,
		Slug:         cfg.PersonaSlug,
		PersonaID:    p.ID.String(),
		Interval:     cfg.Registry.HeartbeatInterval,
		Log:          log,
	}
	regCtx, regCancel := context.WithCancel(ctx)
	defer regCancel()
	go regClient.Run(regCtx)

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		// ReadTimeout and WriteTimeout stay unset: A2A's SendStreamingMessage
		// holds the connection open for the whole reply, and either timeout
		// would cut it off mid-turn — same reasoning as the moderator's SSE
		// server.
		IdleTimeout: 60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.Addr, "persona_slug", cfg.PersonaSlug, "public_url", cfg.A2A.PublicURL)
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
