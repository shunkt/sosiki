// Package agentapi is the discovery pod's HTTP surface: persona pods
// register (and re-register) themselves here, and the moderator lists who's
// currently present.
package agentapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shun/kaigi/backend/internal/registry"
)

// cardResolveTimeout bounds how long resolving a persona pod's own card can
// take during registration. Kept well under the registration heartbeat
// interval so an unresponsive persona pod cannot pile up blocked
// registration handlers — see the plan's GOTCHA on DefaultResolver's 30s
// default being too generous for a 15s heartbeat cadence.
const cardResolveTimeout = 5 * time.Second

// healthCheckTimeout bounds the dependency probe so a stalled database
// cannot hang the whole health check indefinitely — mirrors internal/api's
// handleHealth.
const healthCheckTimeout = 2 * time.Second

// cardResolver is the narrow seam over agentcard.Resolver.Resolve, kept
// local so handler tests can avoid a live HTTP round trip to a persona pod.
type cardResolver interface {
	Resolve(ctx context.Context, baseURL string) (*a2a.AgentCard, error)
}

// Deps are the dependencies NewHandler wires into routes.
type Deps struct {
	Log      *slog.Logger
	Pool     *pgxpool.Pool
	Registry *registry.Store
	Resolver cardResolver
	// TTL is how long a registration counts as present after its last
	// heartbeat — see registry.Agent.Present.
	TTL time.Duration
}

type handlers struct {
	deps Deps
}

// NewHandler builds the discovery pod's HTTP handler.
func NewHandler(d Deps) http.Handler {
	h := &handlers{deps: d}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", h.handleHealth)
	mux.HandleFunc("POST /registry/agents", h.handleRegister)
	mux.HandleFunc("GET /registry/agents", h.handleList)
	mux.HandleFunc("GET /registry/agents/{slug}", h.handleGet)

	return requestLogger(d.Log, mux)
}

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

type registerRequest struct {
	Slug      string `json:"slug"`
	BaseURL   string `json:"baseUrl"`
	PersonaID string `json:"personaId"`
}

// handleRegister resolves the agent's OWN card from baseUrl rather than
// trusting a self-reported one in the request body — the registry is
// supposed to answer "what can this agent actually do", and only asking the
// agent itself keeps that answer honest.
func (h *handlers) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if req.Slug == "" || req.BaseURL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "slug and baseUrl are required"})
		return
	}
	personaID, err := uuid.Parse(req.PersonaID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid personaId"})
		return
	}

	resolveCtx, cancel := context.WithTimeout(r.Context(), cardResolveTimeout)
	defer cancel()
	card, err := h.deps.Resolver.Resolve(resolveCtx, req.BaseURL)
	if err != nil {
		h.deps.Log.Warn("resolve agent card failed", "slug", req.Slug, "base_url", req.BaseURL, "error", err)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "failed to resolve agent card at baseUrl"})
		return
	}

	agent := registry.Agent{
		Slug:      req.Slug,
		BaseURL:   req.BaseURL,
		PersonaID: personaID,
		Card:      card,
	}
	if err := h.deps.Registry.Upsert(r.Context(), agent); err != nil {
		h.deps.Log.Error("upsert agent failed", "slug", req.Slug, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to register agent"})
		return
	}
	writeJSON(w, http.StatusCreated, toAgentDTO(agent, true))
}

func (h *handlers) handleList(w http.ResponseWriter, r *http.Request) {
	agents, err := h.deps.Registry.List(r.Context())
	if err != nil {
		h.deps.Log.Error("list agents failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to list agents"})
		return
	}
	now := time.Now()
	dtos := make([]agentDTO, len(agents))
	for i, a := range agents {
		dtos[i] = toAgentDTO(a, a.Present(now, h.deps.TTL))
	}
	writeJSON(w, http.StatusOK, dtos)
}

func (h *handlers) handleGet(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	a, err := h.deps.Registry.Get(r.Context(), slug)
	if err != nil {
		if errors.Is(err, registry.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "agent not found"})
			return
		}
		h.deps.Log.Error("get agent failed", "slug", slug, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to get agent"})
		return
	}
	writeJSON(w, http.StatusOK, toAgentDTO(a, a.Present(time.Now(), h.deps.TTL)))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("encode response", "error", err)
	}
}

func requestLogger(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		log.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration", time.Since(start),
		)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}
