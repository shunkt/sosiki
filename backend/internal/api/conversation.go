package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/shun/kaigi/backend/internal/chat"
)

type messageDTO struct {
	ID        string `json:"id"`
	Role      string `json:"role"`
	Content   string `json:"content"`
	CreatedAt string `json:"createdAt"`
}

func toMessageDTO(m chat.Message) messageDTO {
	return messageDTO{
		ID:        m.ID.String(),
		Role:      m.Role,
		Content:   m.Content,
		CreatedAt: m.CreatedAt.Format(time.RFC3339),
	}
}

type conversationDTO struct {
	ID        string       `json:"id"`
	PersonaID string       `json:"personaId"`
	Title     string       `json:"title"`
	Messages  []messageDTO `json:"messages"`
}

type createConversationRequest struct {
	PersonaID string `json:"personaId"`
	Title     string `json:"title"`
}

func (h *handlers) handleCreateConversation(w http.ResponseWriter, r *http.Request) {
	var req createConversationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	personaID, err := uuid.Parse(req.PersonaID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid personaId"})
		return
	}

	id, err := h.deps.ChatStore.CreateConversation(r.Context(), personaID, req.Title)
	if err != nil {
		h.deps.Log.Error("create conversation", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to create conversation"})
		return
	}
	writeJSON(w, http.StatusCreated, conversationDTO{
		ID: id.String(), PersonaID: personaID.String(), Title: req.Title, Messages: []messageDTO{},
	})
}

func (h *handlers) handleGetConversation(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid conversation id"})
		return
	}
	c, err := h.deps.ChatStore.GetConversation(r.Context(), id)
	if err != nil {
		if errors.Is(err, chat.ErrConversationNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "conversation not found"})
			return
		}
		h.deps.Log.Error("get conversation", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to get conversation"})
		return
	}

	messages := make([]messageDTO, len(c.Messages))
	for i, m := range c.Messages {
		messages[i] = toMessageDTO(m)
	}
	writeJSON(w, http.StatusOK, conversationDTO{
		ID: c.ID.String(), PersonaID: c.PersonaID.String(), Title: c.Title, Messages: messages,
	})
}

// handleGetDocument resolves a documents.id to a short-lived presigned MinIO
// URL and redirects there, rather than proxying the object through the
// backend. In the normal chat flow the frontend never calls this route — the
// SSE sources event already carries a presigned url per citation — so this
// exists only for direct linking to a document by ID.
func (h *handlers) handleGetDocument(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid document id"})
		return
	}
	key, err := h.deps.ChatStore.DocumentObjectKey(r.Context(), id)
	if err != nil {
		if errors.Is(err, chat.ErrDocumentNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "document not found"})
			return
		}
		h.deps.Log.Error("resolve document", "id", id, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to resolve document"})
		return
	}
	url, err := h.deps.Objects.PresignedURL(r.Context(), key, 15*time.Minute)
	if err != nil {
		h.deps.Log.Error("presign document", "key", key, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to resolve document"})
		return
	}
	http.Redirect(w, r, url, http.StatusFound)
}
