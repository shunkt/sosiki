package meeting

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
)

// cardSource resolves a participant's agent card via the discovery pod —
// kept as a narrow interface so A2ADialer's HTTP behavior can be unit
// tested without a live discovery pod.
type cardSource interface {
	CardFor(ctx context.Context, slug string) (*a2a.AgentCard, error)
}

// A2ADialer builds an a2aclient.Client per participant from the card the
// registry resolved, caching by slug: NewFromCard does transport
// negotiation, which is wasted work to repeat on every turn.
type A2ADialer struct {
	mu      sync.Mutex
	clients map[string]*a2aclient.Client
	cards   cardSource
	http    *http.Client
}

func NewA2ADialer(cards cardSource) *A2ADialer {
	return &A2ADialer{
		clients: make(map[string]*a2aclient.Client),
		cards:   cards,
		http: &http.Client{
			// No overall Timeout: SendStreamingMessage holds the connection
			// open for the whole reply, and a blanket Timeout would cut that
			// stream off mid-turn — the same reasoning as the server's
			// unset http.Server.WriteTimeout elsewhere in this codebase.
			// ResponseHeaderTimeout below bounds only the time to first byte.
			Transport: &http.Transport{ResponseHeaderTimeout: 30 * time.Second},
		},
	}
}

// Dial returns a cached client for p.Slug, or builds one by resolving its
// card through the registry. On a connection error from a stale cached
// client, the caller gets that error back — Dial itself does not retry;
// discarding the cache entry is the caller's job on the next attempt if it
// wants a fresh Dial.
func (d *A2ADialer) Dial(ctx context.Context, p Participant) (agentClient, error) {
	d.mu.Lock()
	if c, ok := d.clients[p.Slug]; ok {
		d.mu.Unlock()
		return c, nil
	}
	d.mu.Unlock()

	card, err := d.cards.CardFor(ctx, p.Slug)
	if err != nil {
		return nil, fmt.Errorf("dialer: resolve card for %s: %w", p.Slug, err)
	}

	client, err := a2aclient.NewFromCard(ctx, card, a2aclient.WithJSONRPCTransport(d.http))
	if err != nil {
		return nil, fmt.Errorf("dialer: build client for %s: %w", p.Slug, err)
	}

	d.mu.Lock()
	d.clients[p.Slug] = client
	d.mu.Unlock()
	return client, nil
}

// Forget drops a cached client, so the next Dial resolves a fresh one — for
// use after a connection error suggests the persona pod restarted (its old
// connection is dead but its card and URL are unchanged).
func (d *A2ADialer) Forget(slug string) {
	d.mu.Lock()
	delete(d.clients, slug)
	d.mu.Unlock()
}

// registryCardSource is the production cardSource: it asks the discovery
// pod's GET /registry/agents/{slug} for the agent's card.
type registryCardSource struct {
	http         *http.Client
	discoveryURL string
}

func NewRegistryCardSource(discoveryURL string) cardSource {
	return &registryCardSource{
		http:         &http.Client{Timeout: 5 * time.Second},
		discoveryURL: discoveryURL,
	}
}

func (r *registryCardSource) CardFor(ctx context.Context, slug string) (*a2a.AgentCard, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		r.discoveryURL+"/registry/agents/"+slug, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	resp, err := r.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("registry returned status %d for %s", resp.StatusCode, slug)
	}

	var dto struct {
		Card *a2a.AgentCard `json:"card"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&dto); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if dto.Card == nil {
		return nil, fmt.Errorf("registry response for %s has no card", slug)
	}
	return dto.Card, nil
}
