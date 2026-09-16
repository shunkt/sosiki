package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/shun/kaigi/backend/internal/meeting"
)

type citationDTO struct {
	ChunkID    string  `json:"chunkId"`
	DocumentID string  `json:"documentId"`
	Title      string  `json:"title"`
	URL        string  `json:"url"`
	Relevance  float32 `json:"relevance"`
	Affinity   float32 `json:"affinity"`
}

func toCitationDTO(c meeting.Citation) citationDTO {
	return citationDTO{
		ChunkID: c.ChunkID.String(), DocumentID: c.DocumentID.String(),
		Title: c.Title, URL: c.URL, Relevance: c.Relevance, Affinity: c.Affinity,
	}
}

type turnDTO struct {
	ID          string        `json:"id"`
	Seq         int           `json:"seq"`
	Round       int           `json:"round"`
	Role        string        `json:"role"`
	SpeakerSlug string        `json:"speakerSlug"`
	SpeakerName string        `json:"speakerName"`
	Content     string        `json:"content"`
	Citations   []citationDTO `json:"citations,omitempty"`
	CreatedAt   string        `json:"createdAt"`
}

func toTurnDTO(t meeting.Turn) turnDTO {
	dto := turnDTO{
		ID: t.ID.String(), Seq: t.Seq, Round: t.Round, Role: t.Role,
		SpeakerSlug: t.SpeakerSlug, SpeakerName: t.SpeakerName, Content: t.Content,
		CreatedAt: t.CreatedAt.Format(time.RFC3339),
	}
	if len(t.Citations) > 0 {
		dto.Citations = make([]citationDTO, len(t.Citations))
		for i, c := range t.Citations {
			dto.Citations[i] = toCitationDTO(c)
		}
	}
	return dto
}

type participantDTO struct {
	Slug          string `json:"slug"`
	Name          string `json:"name"`
	SpeakingOrder int    `json:"speakingOrder"`
}

type meetingDTO struct {
	ID           string           `json:"id"`
	Topic        string           `json:"topic"`
	Participants []participantDTO `json:"participants"`
	Turns        []turnDTO        `json:"turns"`
}

func toMeetingDTO(m meeting.Meeting) meetingDTO {
	participants := make([]participantDTO, len(m.Participants))
	for i, p := range m.Participants {
		participants[i] = participantDTO{Slug: p.Slug, Name: p.Name, SpeakingOrder: p.SpeakingOrder}
	}
	turns := make([]turnDTO, len(m.Turns))
	for i, t := range m.Turns {
		turns[i] = toTurnDTO(t)
	}
	return meetingDTO{ID: m.ID.String(), Topic: m.Topic, Participants: participants, Turns: turns}
}

type createMeetingRequest struct {
	Topic        string   `json:"topic"`
	PersonaSlugs []string `json:"personaSlugs"`
}

// handleCreateMeeting validates every requested slug against the discovery
// pod before creating the meeting: a meeting whose participant is not
// actually present would fail its first turn anyway, so this rejects it up
// front with a clear error instead.
func (h *handlers) handleCreateMeeting(w http.ResponseWriter, r *http.Request) {
	var req createMeetingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if len(req.PersonaSlugs) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "personaSlugs must not be empty"})
		return
	}

	agents, err := h.deps.Agents.ListAgents(r.Context())
	if err != nil {
		h.deps.Log.Error("list agents for meeting creation", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to resolve personas"})
		return
	}
	type agentInfo struct {
		Name    string
		Present bool
	}
	bySlug := make(map[string]agentInfo, len(agents))
	for _, a := range agents {
		bySlug[a.Slug] = agentInfo{Name: a.Name, Present: a.Present}
	}

	participants := make([]meeting.Participant, len(req.PersonaSlugs))
	for i, slug := range req.PersonaSlugs {
		a, ok := bySlug[slug]
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown persona: " + slug})
			return
		}
		if !a.Present {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "persona is not present: " + slug})
			return
		}
		participants[i] = meeting.Participant{Slug: slug, Name: a.Name}
	}

	id, err := h.deps.Meetings.Create(r.Context(), req.Topic, participants)
	if err != nil {
		h.deps.Log.Error("create meeting", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to create meeting"})
		return
	}

	m, err := h.deps.Meetings.Get(r.Context(), id)
	if err != nil {
		h.deps.Log.Error("reload created meeting", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load meeting"})
		return
	}
	writeJSON(w, http.StatusCreated, toMeetingDTO(m))
}

func (h *handlers) handleGetMeeting(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid meeting id"})
		return
	}
	m, err := h.deps.Meetings.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, meeting.ErrMeetingNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "meeting not found"})
			return
		}
		h.deps.Log.Error("get meeting", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to get meeting"})
		return
	}
	writeJSON(w, http.StatusOK, toMeetingDTO(m))
}
