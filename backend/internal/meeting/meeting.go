// Package meeting is the moderator's domain: a meeting has participants
// (personas, by slug) and turns (statements, by the human or a persona),
// persisted to kaigi_meeting. Unlike a persona pod, the moderator DOES hold
// conversation state — see the plan's DB-per-function split.
package meeting

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

// Participant is one persona seated at a meeting, in the order it speaks.
//
// JSON tags matter here even though internal/api has its own DTO layer for
// the REST endpoints: meeting.Event (below) is marshaled directly for the
// SSE stream, with no DTO in between, so Turn/Citation's own tags are what
// the frontend actually receives from speaker_end/sources frames. Without
// them the field names would default to Go's PascalCase and silently
// mismatch the REST API's camelCase — a real inconsistency caught by
// inspecting an actual captured SSE stream, not by any test (nothing
// asserts on exact JSON key casing).
type Participant struct {
	Slug          string `json:"slug"`
	Name          string `json:"name"`
	BaseURL       string `json:"baseUrl"`
	SpeakingOrder int    `json:"speakingOrder"`
}

// Citation is one piece of retrieved evidence behind a persona's turn. It
// carries no foreign key to kaigi_knowledge — see turn_citations' schema
// comment for why — so every field the client needs to render a citation is
// copied here directly rather than joined at read time.
type Citation struct {
	ChunkID    uuid.UUID `json:"chunkId"`
	DocumentID uuid.UUID `json:"documentId"`
	Rank       int       `json:"rank"`
	Title      string    `json:"title"`
	ObjectKey  string    `json:"objectKey"`
	URL        string    `json:"url"`
	Relevance  float32   `json:"relevance"`
	Affinity   float32   `json:"affinity"`
}

// Turn is one statement in the meeting: the human's opening topic (Round 0,
// SpeakerSlug empty) or one persona's reply in a later round.
type Turn struct {
	ID          uuid.UUID  `json:"id"`
	Seq         int        `json:"seq"`
	Round       int        `json:"round"`
	Role        string     `json:"role"`        // "user" | "persona"
	SpeakerSlug string     `json:"speakerSlug"` // "" for the human
	SpeakerName string     `json:"speakerName"`
	Content     string     `json:"content"`
	Citations   []Citation `json:"citations,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
}

type Meeting struct {
	ID           uuid.UUID     `json:"id"`
	Topic        string        `json:"topic"`
	Participants []Participant `json:"participants"`
	Turns        []Turn        `json:"turns"`
}

// Event is one unit of progress the HTTP layer turns into an SSE frame.
// Unlike chat.Event it carries a speaker: a meeting has many, and the
// client needs to know whose turn is currently streaming.
type Event struct {
	Type        string     `json:"type"` // speaker_start|sources|token|speaker_end|speaker_error|round_end|done|error
	Round       int        `json:"round,omitempty"`
	PersonaSlug string     `json:"personaSlug,omitempty"`
	PersonaName string     `json:"personaName,omitempty"`
	Text        string     `json:"text,omitempty"`
	Citations   []Citation `json:"citations,omitempty"`
	Turn        *Turn      `json:"turn,omitempty"`
	Error       string     `json:"error,omitempty"`
}

var (
	ErrMeetingNotFound = errors.New("meeting: not found")
	ErrNoParticipants  = errors.New("meeting: at least one participant is required")
)
