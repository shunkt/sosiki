package registry

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
)

// registerRequest is the wire shape POSTed to the discovery pod's
// POST /registry/agents. The discovery pod resolves the card itself (via
// agentcard.DefaultResolver against BaseURL) rather than trusting a
// self-reported card — see internal/agentapi.
type registerRequest struct {
	Slug      string `json:"slug"`
	BaseURL   string `json:"baseUrl"`
	PersonaID string `json:"personaId"`
}

// Client keeps one persona pod's entry in the registry alive. Registration
// is retried indefinitely rather than fatal: a persona that boots before the
// discovery pod is ready must not crash-loop, it must keep knocking.
type Client struct {
	HTTP *http.Client
	// DiscoveryURL is where registration requests are sent — the discovery
	// pod's own address, e.g. http://discovery:8081.
	DiscoveryURL string
	// SelfURL is this persona pod's OWN address, as reachable by OTHER pods
	// (never "localhost") — what gets registered, not where the request goes.
	// Corresponds to config.A2AConfig.PublicURL.
	SelfURL   string
	Slug      string
	PersonaID string
	Interval  time.Duration
	Log       *slog.Logger
}

// Run registers immediately, then re-registers every Interval until ctx is
// canceled. It never returns an error — every failure is logged and
// retried on the next tick, since a persona pod's job (serving A2A
// requests) does not depend on registry availability to function.
func (c *Client) Run(ctx context.Context) {
	c.registerOnce(ctx)

	ticker := time.NewTicker(c.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.registerOnce(ctx)
		}
	}
}

func (c *Client) registerOnce(ctx context.Context) {
	body, err := json.Marshal(registerRequest{
		Slug:      c.Slug,
		BaseURL:   c.SelfURL,
		PersonaID: c.PersonaID,
	})
	if err != nil {
		c.Log.Warn("registry: encode registration failed", "error", err)
		return
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.DiscoveryURL+"/registry/agents", bytes.NewReader(body))
	if err != nil {
		c.Log.Warn("registry: build request failed", "error", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		c.Log.Warn("registry: register request failed", "error", err, "slug", c.Slug)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		c.Log.Warn("registry: register rejected", "status", resp.StatusCode, "slug", c.Slug)
		return
	}
	c.Log.Info("registry: registered", "slug", c.Slug)
}

// AgentDTO is the shape the discovery pod returns from GET
// /registry/agents{,/{slug}} — kept here (not in internal/agentapi) so both
// the persona-pod-side client and the moderator-side dialer share one
// definition of what a registry response looks like.
type AgentDTO struct {
	Slug      string         `json:"slug"`
	Name      string         `json:"name"`
	PersonaID string         `json:"personaId"`
	BaseURL   string         `json:"baseUrl"`
	Present   bool           `json:"present"`
	Card      *a2a.AgentCard `json:"card"`
}
