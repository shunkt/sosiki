package a2aconv

import (
	"sort"

	"github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/shun/kaigi/backend/internal/persona"
)

// cardVersion is the AgentCard's own version field — the persona-as-an-agent
// contract's version, not the A2A protocol version (that comes from
// a2a.NewAgentInterface, which stamps a2a.Version automatically).
const cardVersion = "1.0.0"

// PersonaCard renders a persona as the A2A manifest other agents discover it
// by. Positive-weight interests become skill tags: that is what makes the
// registry's card listing useful for a human picking meeting participants —
// see the plan's UX Design.
func PersonaCard(p persona.Persona, publicURL string) *a2a.AgentCard {
	tags := make([]string, 0, len(p.Personality.Interests))
	for _, in := range p.Personality.Interests {
		if in.Weight > 0 {
			tags = append(tags, in.Topic)
		}
	}
	// Sorted so the card is byte-identical across restarts for the same
	// persona — interests come back from Postgres in no guaranteed order.
	sort.Strings(tags)

	return &a2a.AgentCard{
		Name:        p.Name,
		Description: p.Personality.Stance,
		Version:     cardVersion,
		SupportedInterfaces: []*a2a.AgentInterface{
			// NewAgentInterface, not a struct literal: it is what stamps
			// ProtocolVersion with a2a.Version — see the plan's GOTCHA.
			a2a.NewAgentInterface(publicURL, a2a.TransportProtocolJSONRPC),
		},
		Capabilities: a2a.AgentCapabilities{
			Streaming:         true,
			PushNotifications: false,
			ExtendedAgentCard: false,
		},
		Skills: []a2a.AgentSkill{{
			ID:          "opinion",
			Name:        "会議での意見表明",
			Description: "知識ベースを自分の関心と懐疑度で検索し、会議録を踏まえて意見を述べる",
			Tags:        tags,
			InputModes:  []string{"text/plain"},
			OutputModes: []string{"text/plain"},
		}},
		DefaultInputModes:  []string{"text/plain"},
		DefaultOutputModes: []string{"text/plain"},
	}
}
