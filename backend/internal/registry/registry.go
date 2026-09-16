// Package registry is the discovery pod's agent catalog: which persona pods
// exist, whether they're currently reachable, and their A2A cards — the
// piece Kubernetes Service DNS does not answer (see the plan's Notes on why
// this pod exists at all).
package registry

import (
	"errors"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/google/uuid"
)

// Agent is one registered persona pod. Present is derived, not stored: a pod
// that stops heartbeating is absent the moment its TTL lapses, with no
// writer needed to mark it so.
type Agent struct {
	Slug       string
	BaseURL    string
	PersonaID  uuid.UUID
	Card       *a2a.AgentCard
	LastSeenAt time.Time
}

// Present reports whether the agent's last heartbeat is still within ttl of
// now. Equal to ttl counts as present — the boundary belongs to the agent,
// not against it.
func (a Agent) Present(now time.Time, ttl time.Duration) bool {
	return now.Sub(a.LastSeenAt) <= ttl
}

// ErrNotFound is returned when no agent is registered under the given slug.
var ErrNotFound = errors.New("registry: agent not found")
