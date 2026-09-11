// Package api wires the HTTP routes and handlers for the kaigi backend.
package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/shun/kaigi/backend/internal/chat"
	"github.com/shun/kaigi/backend/internal/config"
	"github.com/shun/kaigi/backend/internal/objectstore"
	"github.com/shun/kaigi/backend/internal/persona"
)

// chatEngine is the one seam interfaced here (satisfied by *chat.Engine):
// the SSE handler's frame-by-frame behavior needs to be testable without a
// live DeepSeek/Postgres/MinIO stack. Personas/ChatStore/Objects stay
// concrete — their handlers are covered by the integration tests already
// established for their packages (persona, chat, objectstore), not fakes.
type chatEngine interface {
	Reply(ctx context.Context, conversationID uuid.UUID, utterance string, out chan<- chat.Event) error
}

// Deps are the dependencies NewHandler wires into routes.
type Deps struct {
	Log       *slog.Logger
	Personas  *persona.Store
	Chat      chatEngine
	ChatStore *chat.Store
	Objects   *objectstore.Store
}

type handlers struct {
	cfg  config.Config
	deps Deps
}

// NewHandler builds the fully wrapped HTTP handler for the service.
func NewHandler(cfg config.Config, d Deps) http.Handler {
	h := &handlers{cfg: cfg, deps: d}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", h.handleHealth)

	mux.HandleFunc("GET /api/personas", h.handleListPersonas)
	mux.HandleFunc("POST /api/personas", h.handleCreatePersona)
	mux.HandleFunc("GET /api/personas/{id}", h.handleGetPersona)
	mux.HandleFunc("PATCH /api/personas/{id}", h.handleUpdatePersona)

	mux.HandleFunc("POST /api/conversations", h.handleCreateConversation)
	mux.HandleFunc("GET /api/conversations/{id}", h.handleGetConversation)
	mux.HandleFunc("POST /api/conversations/{id}/messages", h.handleSendMessage)

	mux.HandleFunc("GET /api/documents/{id}", h.handleGetDocument)

	return requestLogger(d.Log, cors(cfg.AllowedOrigins, mux))
}

// healthCheckTimeout bounds each dependency probe so a stalled TEI cannot
// hang the whole health check indefinitely.
const healthCheckTimeout = 2 * time.Second

func (h *handlers) handleHealth(w http.ResponseWriter, r *http.Request) {
	status := map[string]string{"status": "ok"}

	// A dependency being unreachable makes the response "degraded", not a
	// 5xx: the process itself is healthy even if a downstream isn't, and a
	// failing health check must not be what takes the container down.
	degraded := false
	check := func(name, url string) {
		ctx, cancel := context.WithTimeout(r.Context(), healthCheckTimeout)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			status[name] = "error"
			degraded = true
			return
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil || resp.StatusCode != http.StatusOK {
			status[name] = "unreachable"
			degraded = true
			return
		}
		_ = resp.Body.Close()
		status[name] = "ok"
	}

	check("embed", h.cfg.EmbedURL+"/health")
	check("rerank", h.cfg.RerankURL+"/health")

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
