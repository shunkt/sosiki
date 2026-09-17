package meeting

import (
	"context"
	"fmt"
	"iter"
	"log/slog"
	"strings"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/google/uuid"

	"github.com/shun/kaigi/backend/internal/a2aconv"
)

// agentClient is the narrow seam over *a2aclient.Client so the round loop —
// the core of what makes this a meeting rather than N independent answers —
// can be unit tested without live persona pods.
type agentClient interface {
	SendStreamingMessage(ctx context.Context, req *a2a.SendMessageRequest) iter.Seq2[a2a.Event, error]
}

// agentDialer resolves a participant to a client. Separate from agentClient
// so a fake can hand back per-persona scripted streams.
type agentDialer interface {
	Dial(ctx context.Context, p Participant) (agentClient, error)
}

type Moderator struct {
	store     *Store
	dialer    agentDialer
	log       *slog.Logger
	maxRounds int
}

func NewModerator(store *Store, dialer agentDialer, log *slog.Logger, maxRounds int) *Moderator {
	return &Moderator{store: store, dialer: dialer, log: log, maxRounds: maxRounds}
}

// Run drives one meeting turn: for each round, every participant speaks
// once, in speaking_order, and each one receives every statement made
// before it — including statements made earlier in the SAME round. That is
// what lets personas react to each other (see the plan's Notes on the
// star-topology rationale), so the participant loop MUST stay sequential:
// parallelizing it would give every participant in a round the same
// snapshot of the transcript, and they would stop being able to respond to
// one another within that round.
//
// A single participant's failure (dial error, stream error, or the persona
// itself reporting TaskStateFailed) does not abort the meeting — it emits a
// speaker_error event and the loop moves on to the next participant. Run
// only returns an error when the meeting cannot proceed at all (not found,
// no participants, the human's own turn failed to save, or the client
// disconnected — see sendEvent).
func (m *Moderator) Run(ctx context.Context, meetingID uuid.UUID, utterance string, rounds int, out chan<- Event) error {
	if rounds < 1 {
		rounds = 1
	}
	if rounds > m.maxRounds {
		rounds = m.maxRounds
	}

	mtg, err := m.store.Get(ctx, meetingID)
	if err != nil {
		return fmt.Errorf("moderator: load meeting: %w", err)
	}
	if len(mtg.Participants) == 0 {
		return ErrNoParticipants
	}

	// Saved before any generation starts, same rationale as the old
	// chat.Engine.Reply: a client that disconnects mid-turn must not lose
	// the human's own statement.
	if _, err := m.store.AppendTurn(ctx, meetingID, Turn{
		Round: 0, Role: "user", SpeakerName: "user", Content: utterance,
	}); err != nil {
		return fmt.Errorf("moderator: save user turn: %w", err)
	}

	names := participantNames(mtg.Participants)

	for round := 1; round <= rounds; round++ {
		for _, p := range mtg.Participants {
			if !sendEvent(ctx, out, Event{Type: "speaker_start", Round: round, PersonaSlug: p.Slug, PersonaName: p.Name}) {
				return ctx.Err()
			}
			if !m.runOneTurn(ctx, meetingID, round, p, names, utterance, out) {
				return ctx.Err()
			}
		}
		if !sendEvent(ctx, out, Event{Type: "round_end", Round: round}) {
			return ctx.Err()
		}
	}

	sendEvent(ctx, out, Event{Type: "done"})
	return nil
}

// sendEvent sends ev on out, but bails out via ctx instead of blocking
// forever if the consumer has stopped reading (e.g. the browser's SSE
// connection dropped and internal/api/turns.go's handler returned) and the
// buffered channel is full. A plain `out <- ev` here would leak this
// goroutine (and the A2A connections and DB handles it holds) for as long
// as the moderator process runs — ctx cancellation only unblocks operations
// that explicitly check it, never a channel send already parked on a full
// buffer. Caught by code review, not by any existing test.
func sendEvent(ctx context.Context, out chan<- Event, ev Event) bool {
	select {
	case out <- ev:
		return true
	case <-ctx.Done():
		return false
	}
}

// runOneTurn drives one participant's A2A call for one round, translating
// its streamed events into meeting.Events. Any failure here is reported as
// speaker_error and swallowed — see Run's doc comment on partial-failure
// semantics. The bool return is false only when ctx died mid-send (the
// caller should stop the whole meeting then, not just this participant).
func (m *Moderator) runOneTurn(ctx context.Context, meetingID uuid.UUID, round int, p Participant, participants []string, utterance string, out chan<- Event) bool {
	// Re-read on every participant, not once per round: this is what lets a
	// later participant in the same round see an earlier one's statement —
	// see Run's doc comment.
	transcript, err := m.store.Transcript(ctx, meetingID)
	if err != nil {
		return m.speakerError(ctx, out, p, fmt.Sprintf("load transcript: %v", err))
	}

	payload := a2aconv.TranscriptPayload{
		MeetingID:    meetingID.String(),
		Round:        round,
		Participants: participants,
		Transcript:   toWire(transcript),
	}
	msg := a2aconv.NewRequestMessage(payload, utterance)

	client, err := m.dialer.Dial(ctx, p)
	if err != nil {
		return m.speakerError(ctx, out, p, fmt.Sprintf("dial: %v", err))
	}

	var sb strings.Builder
	var citations []a2aconv.CitationWire
	var failed bool
	var failMsg string

	for ev, err := range client.SendStreamingMessage(ctx, &a2a.SendMessageRequest{Message: msg}) {
		if err != nil {
			return m.speakerError(ctx, out, p, fmt.Sprintf("stream: %v", err))
		}
		switch e := ev.(type) {
		case *a2a.TaskArtifactUpdateEvent:
			if e.Artifact == nil {
				continue
			}
			for _, part := range e.Artifact.Parts {
				if payload, ok, cerr := a2aconv.CitationsFrom(part); cerr == nil && ok {
					citations = payload.Citations
					if !sendEvent(ctx, out, Event{Type: "sources", PersonaSlug: p.Slug, Citations: toCitations(citations)}) {
						return false
					}
					continue
				}
				if text := part.Text(); text != "" {
					sb.WriteString(text)
					if !sendEvent(ctx, out, Event{Type: "token", PersonaSlug: p.Slug, Text: text}) {
						return false
					}
				}
			}
		case *a2a.TaskStatusUpdateEvent:
			switch e.Status.State {
			case a2a.TaskStateFailed:
				failed = true
				if e.Status.Message != nil {
					for _, part := range e.Status.Message.Parts {
						failMsg += part.Text()
					}
				}
			case a2a.TaskStateCompleted:
				// handled after the loop below
			}
		case *a2a.Message:
			for _, part := range e.Parts {
				if text := part.Text(); text != "" {
					sb.WriteString(text)
					if !sendEvent(ctx, out, Event{Type: "token", PersonaSlug: p.Slug, Text: text}) {
						return false
					}
				}
			}
		}
	}

	if failed {
		if failMsg == "" {
			failMsg = "persona reported a failure"
		}
		return m.speakerError(ctx, out, p, failMsg)
	}

	saveCtx := context.WithoutCancel(ctx)
	turn, err := m.store.AppendTurn(saveCtx, meetingID, Turn{
		Round: round, Role: "persona", SpeakerSlug: p.Slug, SpeakerName: p.Name, Content: sb.String(),
	})
	if err != nil {
		return m.speakerError(ctx, out, p, fmt.Sprintf("save turn: %v", err))
	}
	if len(citations) > 0 {
		if err := m.store.SaveCitations(saveCtx, turn.ID, toCitations(citations)); err != nil {
			m.log.Error("save citations failed", "meeting_id", meetingID, "persona_slug", p.Slug, "error", err)
			// Not fatal to the turn itself — the reply text is already saved
			// and streamed; losing citation persistence loses attribution on
			// reload, not the turn.
		}
	}
	turn.Citations = toCitations(citations)
	return sendEvent(ctx, out, Event{Type: "speaker_end", PersonaSlug: p.Slug, PersonaName: p.Name, Turn: &turn})
}

func (m *Moderator) speakerError(ctx context.Context, out chan<- Event, p Participant, msg string) bool {
	m.log.Warn("speaker failed", "persona_slug", p.Slug, "error", msg)
	return sendEvent(ctx, out, Event{Type: "speaker_error", PersonaSlug: p.Slug, PersonaName: p.Name, Error: msg})
}

func participantNames(ps []Participant) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Name
	}
	return out
}

func toWire(turns []Turn) []a2aconv.TurnWire {
	out := make([]a2aconv.TurnWire, len(turns))
	for i, t := range turns {
		out[i] = a2aconv.TurnWire{Role: t.Role, SpeakerSlug: t.SpeakerSlug, SpeakerName: t.SpeakerName, Content: t.Content}
	}
	return out
}

func toCitations(cs []a2aconv.CitationWire) []Citation {
	out := make([]Citation, len(cs))
	for i, c := range cs {
		chunkID, _ := uuid.Parse(c.ChunkID)
		docID, _ := uuid.Parse(c.DocumentID)
		out[i] = Citation{
			ChunkID:    chunkID,
			DocumentID: docID,
			Rank:       i,
			Title:      c.Title,
			ObjectKey:  c.ObjectKey,
			URL:        c.URL,
			Relevance:  c.Relevance,
			Affinity:   c.Affinity,
		}
	}
	return out
}
