package agentapi

import "github.com/shun/kaigi/backend/internal/registry"

// toAgentDTO reuses registry.AgentDTO directly rather than keeping a second,
// locally-defined shape — the two used to drift (this package's own
// skillDTO/agentDTO had no Skills-carrying counterpart on the client side,
// silently dropping skills data before it reached the frontend; see the
// code review finding this fixes).
func toAgentDTO(a registry.Agent, present bool) registry.AgentDTO {
	dto := registry.AgentDTO{
		Slug:      a.Slug,
		PersonaID: a.PersonaID.String(),
		BaseURL:   a.BaseURL,
		Present:   present,
		Card:      a.Card,
	}
	if a.Card != nil {
		dto.Name = a.Card.Name
		dto.Skills = make([]registry.Skill, len(a.Card.Skills))
		for i, s := range a.Card.Skills {
			dto.Skills[i] = registry.Skill{ID: s.ID, Name: s.Name, Tags: s.Tags}
		}
		dto.Profile = registry.ProfileFromCard(a.Card)
	}
	return dto
}
