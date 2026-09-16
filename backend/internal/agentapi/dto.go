package agentapi

import (
	"github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/shun/kaigi/backend/internal/registry"
)

type skillDTO struct {
	ID   string   `json:"id"`
	Name string   `json:"name"`
	Tags []string `json:"tags"`
}

type agentDTO struct {
	Slug      string         `json:"slug"`
	Name      string         `json:"name"`
	PersonaID string         `json:"personaId"`
	BaseURL   string         `json:"baseUrl"`
	Present   bool           `json:"present"`
	Skills    []skillDTO     `json:"skills"`
	Card      *a2a.AgentCard `json:"card"`
}

func toAgentDTO(a registry.Agent, present bool) agentDTO {
	dto := agentDTO{
		Slug:      a.Slug,
		PersonaID: a.PersonaID.String(),
		BaseURL:   a.BaseURL,
		Present:   present,
		Card:      a.Card,
	}
	if a.Card != nil {
		dto.Name = a.Card.Name
		dto.Skills = make([]skillDTO, len(a.Card.Skills))
		for i, s := range a.Card.Skills {
			dto.Skills[i] = skillDTO{ID: s.ID, Name: s.Name, Tags: s.Tags}
		}
	}
	return dto
}
