package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/google/uuid"

	"github.com/shun/kaigi/backend/internal/persona"
)

type interestDTO struct {
	Topic  string  `json:"topic"`
	Weight float32 `json:"weight"`
}

type personaDTO struct {
	ID         string        `json:"id"`
	Name       string        `json:"name"`
	Stance     string        `json:"stance"`
	Verbosity  string        `json:"verbosity"`
	Skepticism float32       `json:"skepticism"`
	Interests  []interestDTO `json:"interests"`
}

func toPersonaDTO(p persona.Persona) personaDTO {
	interests := make([]interestDTO, len(p.Personality.Interests))
	for i, in := range p.Personality.Interests {
		interests[i] = interestDTO{Topic: in.Topic, Weight: in.Weight}
	}
	return personaDTO{
		ID:         p.ID.String(),
		Name:       p.Name,
		Stance:     p.Personality.Stance,
		Verbosity:  string(p.Personality.Verbosity),
		Skepticism: p.Personality.Skepticism,
		Interests:  interests,
	}
}

func (h *handlers) handleListPersonas(w http.ResponseWriter, r *http.Request) {
	personas, err := h.deps.Personas.List(r.Context())
	if err != nil {
		h.deps.Log.Error("list personas", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to list personas"})
		return
	}
	dtos := make([]personaDTO, len(personas))
	for i, p := range personas {
		dtos[i] = toPersonaDTO(p)
	}
	writeJSON(w, http.StatusOK, dtos)
}

type createPersonaRequest struct {
	Name       string        `json:"name"`
	Stance     string        `json:"stance"`
	Verbosity  string        `json:"verbosity"`
	Skepticism float32       `json:"skepticism"`
	Interests  []interestDTO `json:"interests"`
}

func (h *handlers) handleCreatePersona(w http.ResponseWriter, r *http.Request) {
	var req createPersonaRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if req.Name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name must not be empty"})
		return
	}

	interests := make([]persona.InterestInput, len(req.Interests))
	for i, in := range req.Interests {
		interests[i] = persona.InterestInput{Topic: in.Topic, Weight: in.Weight}
	}

	p, err := h.deps.Personas.Create(r.Context(), persona.CreateInput{
		Name:       req.Name,
		Stance:     req.Stance,
		Verbosity:  persona.Verbosity(req.Verbosity),
		Skepticism: req.Skepticism,
		Interests:  interests,
	})
	if err != nil {
		h.deps.Log.Error("create persona", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to create persona"})
		return
	}
	writeJSON(w, http.StatusCreated, toPersonaDTO(p))
}

func (h *handlers) handleGetPersona(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid persona id"})
		return
	}
	p, err := h.deps.Personas.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, persona.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "persona not found"})
			return
		}
		h.deps.Log.Error("get persona", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to get persona"})
		return
	}
	writeJSON(w, http.StatusOK, toPersonaDTO(p))
}

type updatePersonaRequest struct {
	Stance     *string  `json:"stance"`
	Verbosity  *string  `json:"verbosity"`
	Skepticism *float32 `json:"skepticism"`
}

func (h *handlers) handleUpdatePersona(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid persona id"})
		return
	}
	var req updatePersonaRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	in := persona.UpdateInput{Stance: req.Stance, Skepticism: req.Skepticism}
	if req.Verbosity != nil {
		v := persona.Verbosity(*req.Verbosity)
		in.Verbosity = &v
	}

	p, err := h.deps.Personas.Update(r.Context(), id, in)
	if err != nil {
		if errors.Is(err, persona.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "persona not found"})
			return
		}
		h.deps.Log.Error("update persona", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to update persona"})
		return
	}
	writeJSON(w, http.StatusOK, toPersonaDTO(p))
}
