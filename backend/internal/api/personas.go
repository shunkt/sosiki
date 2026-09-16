package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/shun/kaigi/backend/internal/registry"
)

func (h *handlers) handleListPersonas(w http.ResponseWriter, r *http.Request) {
	agents, err := h.deps.Agents.ListAgents(r.Context())
	if err != nil {
		h.deps.Log.Error("list personas (via discovery)", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to list personas"})
		return
	}
	writeJSON(w, http.StatusOK, agents)
}

// discoveryClient is the production agentLister: a thin HTTP client over the
// discovery pod's GET /registry/agents.
type discoveryClient struct {
	http *http.Client
	url  string
}

func NewDiscoveryClient(url string) *discoveryClient {
	return &discoveryClient{http: &http.Client{Timeout: 5 * time.Second}, url: url}
}

func (c *discoveryClient) ListAgents(ctx context.Context) ([]registry.AgentDTO, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url+"/registry/agents", nil)
	if err != nil {
		return nil, fmt.Errorf("discovery: build request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("discovery: request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("discovery: unexpected status %d", resp.StatusCode)
	}

	var agents []registry.AgentDTO
	if err := json.NewDecoder(resp.Body).Decode(&agents); err != nil {
		return nil, fmt.Errorf("discovery: decode response: %w", err)
	}
	return agents, nil
}
