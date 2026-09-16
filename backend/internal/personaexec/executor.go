// Package personaexec adapts chat.Engine to A2A's AgentExecutor interface —
// the bridge that turns a persona from an in-process RAG pipeline into a
// standalone A2A server (cmd/persona).
package personaexec

import (
	"context"
	"fmt"
	"iter"
	"log/slog"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/shun/kaigi/backend/internal/a2aconv"
	"github.com/shun/kaigi/backend/internal/chat"
	"github.com/shun/kaigi/backend/internal/persona"
)

// replyEngine is the narrow seam over *chat.Engine so the event sequence this
// executor emits can be unit tested without a live OpenAI/Postgres/MinIO
// stack — the same seam style internal/api uses for chatEngine.
type replyEngine interface {
	Reply(ctx context.Context, p persona.Persona, participants []string,
		transcript []chat.Turn, utterance string, out chan<- chat.Event) error
}

// Executor implements a2asrv.AgentExecutor for one fixed persona. A process
// running this serves exactly one persona (see cmd/persona's PERSONA_SLUG) —
// the persona is baked in at construction, not read per-request.
type Executor struct {
	engine  replyEngine
	persona persona.Persona
	log     *slog.Logger
}

func New(engine replyEngine, p persona.Persona, log *slog.Logger) *Executor {
	return &Executor{engine: engine, persona: p, log: log}
}

// Execute drives one A2A turn: parse the transcript out of the request
// message, run chat.Engine.Reply, and translate its Events into the A2A
// event sequence submitted -> working -> artifact(citations) ->
// artifact(tokens)* -> completed|failed.
func (e *Executor) Execute(ctx context.Context, execCtx *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		payload, utterance, err := a2aconv.TranscriptFrom(execCtx.Message)
		if err != nil {
			yield(nil, fmt.Errorf("personaexec: parse request: %w", err))
			return
		}

		if !yield(a2a.NewSubmittedTask(execCtx, execCtx.Message), nil) {
			return
		}
		if !yield(a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateWorking, nil), nil) {
			return
		}

		// If the consumer (the moderator's A2A client) stops ranging over
		// this iterator early — yield returns false below — runCtx must be
		// canceled so Reply's goroutine does not leak waiting on OpenAI/MinIO
		// forever. See the plan's GOTCHA on this exact failure mode.
		runCtx, cancel := context.WithCancel(ctx)
		defer cancel()

		events := make(chan chat.Event, 16)
		go func() {
			defer close(events)
			transcript := toTurns(payload.Transcript)
			if err := e.engine.Reply(runCtx, e.persona, payload.Participants,
				transcript, utterance, events); err != nil {
				e.log.Error("reply failed", "meeting_id", payload.MeetingID, "error", err)
				events <- chat.Event{Type: "error", Error: err.Error()}
			}
		}()

		// replyArtifactID is set once the FIRST token establishes the reply
		// artifact via NewArtifactEvent; every subsequent token appends to
		// that same ID via NewArtifactUpdateEvent. Calling
		// NewArtifactUpdateEvent before any NewArtifactEvent for that ID
		// exists fails server-side ("no artifact found for update"), which
		// then cancels the whole task — this was caught by an actual A2A
		// round trip (unit tests build a2a.Event values directly and never
		// exercise the SDK's own artifact bookkeeping), not by any test in
		// this package.
		var replyArtifactID a2a.ArtifactID
		var replyArtifactStarted bool
		for ev := range events {
			switch ev.Type {
			case "sources":
				part := a2aconv.NewCitationsPart(toCitationWires(ev.Sources))
				if !yield(a2a.NewArtifactEvent(execCtx, part), nil) {
					return
				}
			case "token":
				if !replyArtifactStarted {
					artifactEvent := a2a.NewArtifactEvent(execCtx, a2a.NewTextPart(ev.Text))
					replyArtifactID = artifactEvent.Artifact.ID
					replyArtifactStarted = true
					if !yield(artifactEvent, nil) {
						return
					}
					continue
				}
				if !yield(a2a.NewArtifactUpdateEvent(execCtx, replyArtifactID,
					a2a.NewTextPart(ev.Text)), nil) {
					return
				}
			case "error":
				yield(a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateFailed,
					a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart(ev.Error))), nil)
				return
			case "done":
				yield(a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateCompleted, nil), nil)
				return
			}
		}
	}
}

// Cancel reports cancellation immediately — chat.Engine.Reply has no
// cancellation-specific cleanup beyond what ctx already does (the SDK
// cancels the Execute ctx on its own cancellation path).
func (e *Executor) Cancel(_ context.Context, execCtx *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		yield(a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateCanceled, nil), nil)
	}
}

func toTurns(wires []a2aconv.TurnWire) []chat.Turn {
	out := make([]chat.Turn, len(wires))
	for i, w := range wires {
		out[i] = chat.Turn{Role: w.Role, SpeakerName: w.SpeakerName, Content: w.Content}
	}
	return out
}

func toCitationWires(sources []chat.Source) []a2aconv.CitationWire {
	out := make([]a2aconv.CitationWire, len(sources))
	for i, s := range sources {
		out[i] = a2aconv.CitationWire{
			ChunkID:    s.ChunkID.String(),
			DocumentID: s.DocumentID.String(),
			Title:      s.Title,
			URL:        s.URL,
			Relevance:  s.Relevance,
			Affinity:   s.Affinity,
		}
	}
	return out
}
