// Package api wires the HTTP routes and handlers for the moderator: the one
// pod that speaks plain REST+SSE to the browser and A2A to persona pods.
package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shun/kaigi/backend/internal/config"
	"github.com/shun/kaigi/backend/internal/meeting"
	"github.com/shun/kaigi/backend/internal/registry"
)

// moderatorRunner is the one seam interfaced here (satisfied by
// *meeting.Moderator): the SSE handler's frame-by-frame behavior needs to be
// testable without live persona pods — the same pattern the pre-A2A chatEngine
// seam used for chat.Engine.
type moderatorRunner interface {
	Run(ctx context.Context, meetingID uuid.UUID, utterance string, rounds int, out chan<- meeting.Event) error
}

// agentLister is the seam over the discovery pod's HTTP API, so
// handleListPersonas can be tested without a live discovery pod.
type agentLister interface {
	ListAgents(ctx context.Context) ([]registry.AgentDTO, error)
}

// Deps are the dependencies NewHandler wires into routes.
type Deps struct {
	Log       *slog.Logger
	Pool      *pgxpool.Pool // kaigi_meeting
	Meetings  *meeting.Store
	Moderator moderatorRunner
	Agents    agentLister
	// DefaultRounds is used when a turn request omits rounds.
	DefaultRounds int
}

type handlers struct {
	cfg  config.Config
	deps Deps
}

// NewHandler builds the fully wrapped HTTP handler for the moderator.
func NewHandler(cfg config.Config, d Deps) http.Handler {
	h := &handlers{cfg: cfg, deps: d}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", h.handleHealth)

	mux.HandleFunc("GET /api/personas", h.handleListPersonas)

	mux.HandleFunc("POST /api/meetings", h.handleCreateMeeting)
	mux.HandleFunc("GET /api/meetings/{id}", h.handleGetMeeting)
	mux.HandleFunc("POST /api/meetings/{id}/turns", h.handleSendTurn)

	return requestLogger(d.Log, cors(cfg.AllowedOrigins, mux))
}

// healthCheckTimeout bounds the dependency probe so a stalled database
// cannot hang the whole health check indefinitely.
const healthCheckTimeout = 2 * time.Second

func (h *handlers) handleHealth(w http.ResponseWriter, r *http.Request) {
	status := map[string]string{"status": "ok"}
	degraded := false

	switch {
	case h.deps.Pool == nil:
		status["db"] = "unconfigured"
		degraded = true
	default:
		ctx, cancel := context.WithTimeout(r.Context(), healthCheckTimeout)
		defer cancel()
		if err := h.deps.Pool.Ping(ctx); err != nil {
			status["db"] = "unreachable"
			degraded = true
		} else {
			status["db"] = "ok"
		}
	}

	if degraded {
		status["status"] = "degraded"
	}
	writeJSON(w, http.StatusOK, status)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// The status line is already sent, so logging is all that is left.
		slog.Error("encode response", "error", err)
	}
}
