package main

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// healthCheckTimeout bounds the dependency probe so a stalled database
// cannot hang the whole health check indefinitely — same convention as
// internal/api's handleHealth.
const healthCheckTimeout = 2 * time.Second

// healthz reports both pools this persona pod depends on. Degraded (not a
// 5xx) on a probe failure: the process itself may still be able to answer
// A2A calls for cached data, and a failing health check must not be what
// takes the pod down.
func healthz(personaPool, knowledgePool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		status := map[string]string{"status": "ok"}
		degraded := false

		ctx, cancel := context.WithTimeout(r.Context(), healthCheckTimeout)
		defer cancel()

		if err := personaPool.Ping(ctx); err != nil {
			status["persona_db"] = "unreachable"
			degraded = true
		} else {
			status["persona_db"] = "ok"
		}
		if err := knowledgePool.Ping(ctx); err != nil {
			status["knowledge_db"] = "unreachable"
			degraded = true
		} else {
			status["knowledge_db"] = "ok"
		}

		if degraded {
			status["status"] = "degraded"
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(status)
	}
}
