// Package api wires the HTTP routes and handlers for the kaigi backend.
package api

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/shun/kaigi/backend/internal/config"
)

// NewHandler builds the fully wrapped HTTP handler for the service.
func NewHandler(cfg config.Config, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", handleHealth)
	mux.HandleFunc("GET /api/hello", handleHello)

	return requestLogger(log, cors(cfg.AllowedOrigins, mux))
}

func handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func handleHello(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" {
		name = "world"
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "hello, " + name})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// The status line is already sent, so logging is all that is left.
		slog.Error("encode response", "error", err)
	}
}
