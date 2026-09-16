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
type Participant struct {
	Slug          string
	Name          string
	BaseURL       string
	SpeakingOrder int
}

// Citation is one piece of retrieved evidence behind a persona's turn. It
// carries no foreign key to kaigi_knowledge — see turn_citations' schema
// comment for why — so every field the client needs to render a citation is
// copied here directly rather than joined at read time.
type Citation struct {
	ChunkID    uuid.UUID
	DocumentID uuid.UUID
	Rank       int
	Title      string
	ObjectKey  string
	URL        string
	Relevance  float32
	Affinity   float32
}

// Turn is one statement in the meeting: the human's opening topic (Round 0,
// SpeakerSlug empty) or one persona's reply in a later round.
type Turn struct {
	ID          uuid.UUID
	Seq         int
	Round       int
	Role        string // "user" | "persona"
	SpeakerSlug string // "" for the human
	SpeakerName string
	Content     string
	Citations   []Citation
	CreatedAt   time.Time
}

type Meeting struct {
	ID           uuid.UUID
	Topic        string
	Participants []Participant
	Turns        []Turn
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
