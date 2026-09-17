// Package a2aconv converts between kaigi's domain types and A2A wire types
// (a2a.Message, a2a.Part). It is the one place that knows the shape of the
// DataPart payloads the moderator and persona pods exchange, so a change to
// either side's wire format only touches this package.
package a2aconv

import (
	"encoding/gob"
	"encoding/json"
	"fmt"

	"github.com/a2aproject/a2a-go/v2/a2a"
)

// init registers TranscriptPayload and CitationsPayload with encoding/gob.
// a2asrv's default in-memory task store (a2asrv/taskstore/inmemory.go) deep-
// copies every Task via a gob encode/decode round trip on every read and
// write; a DataPart's payload sits behind Part.Content's any-typed Data.Value
// field, and gob refuses to encode a concrete type behind an interface that
// it has not been told about — the request never reaches OpenAI at all, it
// fails inside the SDK's own task-store bookkeeping before the executor's
// events are even recorded. This does not show up in this package's own
// tests (they build a2a types directly, no task store involved) or in
// personaexec's tests (a fake engine, no real a2asrv.Handler) — only an
// actual A2A round trip through a real server surfaces it.
func init() {
	gob.Register(TranscriptPayload{})
	gob.Register(CitationsPayload{})
}

// Metadata key/values that tag a DataPart's payload shape. A2A's Part has no
// built-in discriminator beyond its Go type (Text/Raw/Data/URL), and a
// message can legitimately carry more than one DataPart (transcript request,
// citations response) — these are how a reader picks the right one out.
const (
	MetaKind       = "kaigi.kind"
	KindTranscript = "transcript"
	KindCitations  = "citations"
)

// TurnWire is one prior statement in the meeting, as sent over the wire.
// Mirrors meeting.Turn / chat.Turn but kept separate: this is the A2A
// contract, and the domain types on either side of it are free to evolve
// independently of it.
type TurnWire struct {
	Role string `json:"role"` // "user" | "persona"
	// SpeakerSlug is "" for the human's own turns and is what a receiving
	// persona uses to tell its own prior statements apart from another
	// persona's (see chat/engine.go's toChatMessages/Reply) — SpeakerName
	// alone is not safe for that: personas.slug is UNIQUE but personas.name
	// is not, so two personas could share a display name.
	SpeakerSlug string `json:"speakerSlug"`
	SpeakerName string `json:"speakerName"`
	Content     string `json:"content"`
}

// TranscriptPayload rides in a DataPart alongside the utterance TextPart. A
// persona pod is stateless (see the plan's rationale), so this is the entire
// history it receives for one turn.
type TranscriptPayload struct {
	MeetingID    string     `json:"meetingId"`
	Round        int        `json:"round"`
	Participants []string   `json:"participants"`
	Transcript   []TurnWire `json:"transcript"`
}

// NewRequestMessage builds the ROLE_USER message a moderator sends to a
// persona pod: the utterance as a TextPart (readable on its own by any A2A
// client), plus the full transcript as a tagged DataPart.
func NewRequestMessage(payload TranscriptPayload, utterance string) *a2a.Message {
	data := a2a.NewDataPart(payload)
	data.SetMeta(MetaKind, KindTranscript)
	return a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart(utterance), data)
}

// TranscriptFrom extracts the TranscriptPayload and utterance text from a
// request message. A message with no transcript DataPart is not an error —
// it is the meeting's opening turn (round 1, empty transcript) — so
// TranscriptFrom returns a zero-value payload rather than failing.
func TranscriptFrom(msg *a2a.Message) (TranscriptPayload, string, error) {
	if msg == nil {
		return TranscriptPayload{}, "", fmt.Errorf("a2aconv: nil message")
	}

	var utterance string
	var payload TranscriptPayload
	var found bool
	for _, part := range msg.Parts {
		if part == nil {
			continue
		}
		if kind, _ := part.Metadata[MetaKind].(string); kind == KindTranscript {
			if err := remarshal(part.Data(), &payload); err != nil {
				return TranscriptPayload{}, "", fmt.Errorf("a2aconv: decode transcript: %w", err)
			}
			found = true
			continue
		}
		if text := part.Text(); text != "" {
			utterance = text
		}
	}
	_ = found // no error path needed; documented above as a normal case
	return payload, utterance, nil
}

// CitationWire is one citation as sent from a persona pod back to the
// moderator. Carries the presigned URL already resolved — the persona pod
// holds the MinIO credentials, the moderator does not (see the plan's DB/
// object-store ownership split) — so the moderator only ever stores and
// forwards this, never re-resolves it.
type CitationWire struct {
	ChunkID    string  `json:"chunkId"`
	DocumentID string  `json:"documentId"`
	Title      string  `json:"title"`
	ObjectKey  string  `json:"objectKey"`
	URL        string  `json:"url"`
	Relevance  float32 `json:"relevance"`
	Affinity   float32 `json:"affinity"`
}

// CitationsPayload wraps CitationWire for the DataPart envelope.
type CitationsPayload struct {
	Citations []CitationWire `json:"citations"`
}

// NewCitationsPart builds the tagged DataPart a persona pod's executor sends
// as an artifact event once retrieval has produced sources — see
// personaexec.Executor.Execute, which sends this before any token parts.
func NewCitationsPart(cs []CitationWire) *a2a.Part {
	part := a2a.NewDataPart(CitationsPayload{Citations: cs})
	part.SetMeta(MetaKind, KindCitations)
	return part
}

// CitationsFrom reads a citations payload out of one part, if that part
// carries one. The bool return lets a caller range over an artifact's parts
// and skip non-citation ones without treating a mismatch as an error.
func CitationsFrom(part *a2a.Part) (CitationsPayload, bool, error) {
	if part == nil {
		return CitationsPayload{}, false, nil
	}
	kind, _ := part.Metadata[MetaKind].(string)
	if kind != KindCitations {
		return CitationsPayload{}, false, nil
	}
	var payload CitationsPayload
	if err := remarshal(part.Data(), &payload); err != nil {
		return CitationsPayload{}, false, fmt.Errorf("a2aconv: decode citations: %w", err)
	}
	return payload, true, nil
}

// remarshal re-encodes src as JSON and decodes it into dst. This is
// required, not merely convenient: a DataPart's Data() is a live Go value
// only within the same process (as in a unit test); across a real A2A call
// the JSON-RPC transport has already round-tripped it to map[string]any, and
// a direct type assertion on that value always fails. See the plan's GOTCHA
// on this exact point and a2aconv_test.go's round-trip test that guards it.
func remarshal(src any, dst any) error {
	b, err := json.Marshal(src)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	if err := json.Unmarshal(b, dst); err != nil {
		return fmt.Errorf("unmarshal: %w", err)
	}
	return nil
}
