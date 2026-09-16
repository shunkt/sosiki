package agentapi

import (
	"context"
	"net/http"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient/agentcard"
)

// defaultResolverAdapter wraps agentcard.Resolver with a tighter HTTP
// timeout than agentcard.DefaultResolver's 30 seconds — see
// cardResolveTimeout's doc comment in agentapi.go for why that default is
// too generous against a 15-second heartbeat cadence.
type defaultResolverAdapter struct {
	r *agentcard.Resolver
}

// NewDefaultResolver builds the cardResolver cmd/discovery wires in.
func NewDefaultResolver() cardResolver {
	return &defaultResolverAdapter{
		r: agentcard.NewResolver(&http.Client{Timeout: cardResolveTimeout}),
	}
}

func (d *defaultResolverAdapter) Resolve(ctx context.Context, baseURL string) (*a2a.AgentCard, error) {
	return d.r.Resolve(ctx, baseURL)
}
