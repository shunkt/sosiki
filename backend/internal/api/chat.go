package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/shun/kaigi/backend/internal/chat"
)

// keepAliveInterval bounds how long an idle SSE connection can go without a
// frame before intermediaries (proxies, load balancers) may decide it is
// dead and close it.
const keepAliveInterval = 15 * time.Second

// frameWriteTimeout bounds a single SSE frame write. main.go leaves
// WriteTimeout unset on the server (it would cut the whole stream off), so
// each frame gets its own deadline here instead.
const frameWriteTimeout = 30 * time.Second

type sendMessageRequest struct {
	Content string `json:"content"`
}

func (h *handlers) handleSendMessage(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid conversation id"})
		return
	}

	var req sendMessageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if req.Content == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "content must not be empty"})
		return
	}

	// All headers must be set before the first WriteHeader/Write — once the
	// status line goes out, headers are locked in.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable reverse-proxy buffering
	w.WriteHeader(http.StatusOK)

	rc := http.NewResponseController(w)
	if err := rc.Flush(); err != nil {
		// Unwrap is missing on the wrapping ResponseWriter somewhere in the
		// middleware chain — see statusRecorder.Unwrap in middleware.go.
		h.deps.Log.Error("SSE flush unsupported; response will buffer until handler returns", "error", err)
	}

	events := make(chan chat.Event, 16)
	go func() {
		defer close(events)
		if err := h.deps.Chat.Reply(r.Context(), id, req.Content, events); err != nil {
			h.deps.Log.Error("chat reply failed", "conversation_id", id, "error", err)
			events <- chat.Event{Type: "error", Error: err.Error()}
		}
	}()

	ticker := time.NewTicker(keepAliveInterval)
	defer ticker.Stop()

	for {
		select {
		case ev, ok := <-events:
			if !ok {
				return
			}
			_ = rc.SetWriteDeadline(time.Now().Add(frameWriteTimeout))
			if err := writeSSE(w, ev); err != nil {
				h.deps.Log.Error("SSE write failed", "conversation_id", id, "error", err)
				return
			}
			_ = rc.Flush()

		case <-ticker.C:
			_ = rc.SetWriteDeadline(time.Now().Add(frameWriteTimeout))
			if _, err := fmt.Fprint(w, ": keep-alive\n\n"); err != nil {
				return
			}
			_ = rc.Flush()

		case <-r.Context().Done():
			return
		}
	}
}

// writeSSE encodes ev as JSON in the data field. The value must be
// JSON-encoded, not interpolated as raw text: a token containing a newline
// would otherwise start a new SSE field and corrupt the frame.
func writeSSE(w http.ResponseWriter, ev chat.Event) error {
	body, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, body); err != nil {
		return fmt.Errorf("write frame: %w", err)
	}
	return nil
}
