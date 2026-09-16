package personaexec

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/google/uuid"

	"github.com/shun/kaigi/backend/internal/a2aconv"
	"github.com/shun/kaigi/backend/internal/chat"
	"github.com/shun/kaigi/backend/internal/persona"
)

// fakeEngine drives Execute's event translation with a scripted sequence of
// chat.Events, so tests control exactly what the executor sees without a
// live chat.Engine (OpenAI/Postgres/MinIO).
type fakeEngine struct {
	events   []chat.Event
	err      error
	finished chan struct{} // closed when Reply returns, for leak tests
}

func (f *fakeEngine) Reply(ctx context.Context, _ persona.Persona, _ []string, _ []chat.Turn, _ string, out chan<- chat.Event) error {
	if f.finished != nil {
		defer close(f.finished)
	}
	for _, ev := range f.events {
		select {
		case out <- ev:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return f.err
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func testExecCtx() *a2asrv.ExecutorContext {
	payload := a2aconv.TranscriptPayload{MeetingID: "m1", Participants: []string{"test"}}
	msg := a2aconv.NewRequestMessage(payload, "hello")
	return &a2asrv.ExecutorContext{Message: msg, TaskID: a2a.TaskID(uuid.NewString()), ContextID: "ctx1"}
}

// collect drains an iter.Seq2[a2a.Event, error] into a slice, stopping early
// if limit >= 0 events have been collected (to exercise yield-returns-false).
func collect(t *testing.T, seq func(func(a2a.Event, error) bool), limit int) []a2a.Event {
	t.Helper()
	var events []a2a.Event
	seq(func(ev a2a.Event, err error) bool {
		if err != nil {
			t.Fatalf("unexpected error event: %v", err)
		}
		events = append(events, ev)
		if limit >= 0 && len(events) >= limit {
			return false
		}
		return true
	})
	return events
}

func TestExecutorEventOrder(t *testing.T) {
	engine := &fakeEngine{events: []chat.Event{
		{Type: "sources", Sources: []chat.Source{{ChunkID: uuid.New(), Title: "doc"}}},
		{Type: "token", Text: "こん"},
		{Type: "token", Text: "にちは"},
		{Type: "done", Text: "こんにちは"},
	}}
	exec := New(engine, persona.Persona{Name: "test"}, testLogger())

	events := collect(t, exec.Execute(context.Background(), testExecCtx()), -1)

	if len(events) != 6 {
		t.Fatalf("got %d events, want 6 (submitted, working, artifact(citations), artifact(token), artifact(token), completed): %+v", len(events), events)
	}

	statusAt := func(i int) a2a.TaskState {
		ev, ok := events[i].(*a2a.TaskStatusUpdateEvent)
		if !ok {
			t.Fatalf("events[%d] = %T, want *a2a.TaskStatusUpdateEvent", i, events[i])
		}
		return ev.Status.State
	}
	if _, ok := events[0].(*a2a.Task); !ok {
		t.Errorf("events[0] = %T, want *a2a.Task (submitted)", events[0])
	}
	if got := statusAt(1); got != a2a.TaskStateWorking {
		t.Errorf("events[1] state = %v, want TaskStateWorking", got)
	}
	if _, ok := events[2].(*a2a.TaskArtifactUpdateEvent); !ok {
		t.Errorf("events[2] = %T, want *a2a.TaskArtifactUpdateEvent (citations)", events[2])
	}
	firstToken, ok := events[3].(*a2a.TaskArtifactUpdateEvent)
	if !ok {
		t.Fatalf("events[3] = %T, want *a2a.TaskArtifactUpdateEvent (token)", events[3])
	}
	secondToken, ok := events[4].(*a2a.TaskArtifactUpdateEvent)
	if !ok {
		t.Fatalf("events[4] = %T, want *a2a.TaskArtifactUpdateEvent (token)", events[4])
	}
	if got := statusAt(5); got != a2a.TaskStateCompleted {
		t.Errorf("events[5] state = %v, want TaskStateCompleted", got)
	}

	// This is the exact regression an actual A2A round trip caught that no
	// fake-based test previously exercised: the SDK's server-side task
	// processing rejects an artifact *update* whose ID was never
	// established by a prior artifact *create* event ("no artifact found
	// for update"), silently truncating every reply to zero tokens. The
	// first token must therefore be a create (Append=false, a fresh ID) and
	// every subsequent token an update (Append=true) to THAT SAME ID.
	if firstToken.Append {
		t.Error("first token event has Append=true, want false (it must create the artifact, not update one)")
	}
	if !secondToken.Append {
		t.Error("second token event has Append=false, want true (it must update the artifact the first token created)")
	}
	if firstToken.Artifact.ID != secondToken.Artifact.ID {
		t.Errorf("token artifact IDs differ: first=%v second=%v, want the same ID across all tokens in one reply",
			firstToken.Artifact.ID, secondToken.Artifact.ID)
	}
}

func TestExecutorEngineError(t *testing.T) {
	engine := &fakeEngine{events: []chat.Event{
		{Type: "error", Error: "boom"},
	}}
	exec := New(engine, persona.Persona{Name: "test"}, testLogger())

	events := collect(t, exec.Execute(context.Background(), testExecCtx()), -1)

	last := events[len(events)-1]
	ev, ok := last.(*a2a.TaskStatusUpdateEvent)
	if !ok {
		t.Fatalf("last event = %T, want *a2a.TaskStatusUpdateEvent", last)
	}
	if ev.Status.State != a2a.TaskStateFailed {
		t.Errorf("last event state = %v, want TaskStateFailed", ev.Status.State)
	}
}

// TestExecutorYieldFalseCancelsEngine verifies that when the consumer stops
// ranging over this iterator early (yield returns false), runCtx is
// canceled so Reply's goroutine does not leak. It uses more events than the
// executor's internal buffer (16) can hold: once the consumer stops
// draining, the fake's send on out<- blocks until either the buffer has
// room (it won't — nobody is reading) or ctx is canceled. Only cancellation
// can unblock it, so Reply returning promptly is direct evidence runCtx was
// canceled — see the plan's GOTCHA on this exact failure mode.
func TestExecutorYieldFalseCancelsEngine(t *testing.T) {
	finished := make(chan struct{})
	events := make([]chat.Event, 30)
	for i := range events {
		events[i] = chat.Event{Type: "token", Text: "x"}
	}
	engine := &fakeEngine{events: events, finished: finished}
	exec := New(engine, persona.Persona{Name: "test"}, testLogger())

	// Consume only the first few events (submitted, working, a couple of
	// tokens) then stop ranging — simulating a client that disconnects
	// mid-stream while the engine still has 20+ events queued to send.
	_ = collect(t, exec.Execute(context.Background(), testExecCtx()), 4)

	select {
	case <-finished:
		// engine.Reply returned — it can only have done so by observing
		// ctx.Done(), since the 16-slot buffer cannot hold all 30 events
		// and nothing is draining it anymore.
	case <-time.After(2 * time.Second):
		t.Fatal("engine.Reply did not return after the consumer stopped ranging — runCtx was not canceled (goroutine leak)")
	}
}

func TestExecutorNilMessageErrors(t *testing.T) {
	engine := &fakeEngine{}
	exec := New(engine, persona.Persona{Name: "test"}, testLogger())

	execCtx := &a2asrv.ExecutorContext{Message: nil, TaskID: a2a.TaskID(uuid.NewString())}

	var gotErr error
	exec.Execute(context.Background(), execCtx)(func(ev a2a.Event, err error) bool {
		if err != nil {
			gotErr = err
		}
		return true
	})
	if gotErr == nil {
		t.Error("expected an error event for a nil request message")
	}
}

func TestExecutorCancelEmitsCanceledStatus(t *testing.T) {
	engine := &fakeEngine{}
	exec := New(engine, persona.Persona{Name: "test"}, testLogger())

	events := collect(t, exec.Cancel(context.Background(), testExecCtx()), -1)
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	ev, ok := events[0].(*a2a.TaskStatusUpdateEvent)
	if !ok {
		t.Fatalf("event = %T, want *a2a.TaskStatusUpdateEvent", events[0])
	}
	if ev.Status.State != a2a.TaskStateCanceled {
		t.Errorf("state = %v, want TaskStateCanceled", ev.Status.State)
	}
}

func TestToTurnsPreservesOrder(t *testing.T) {
	wires := []a2aconv.TurnWire{
		{Role: "user", SpeakerName: "user", Content: "a"},
		{Role: "persona", SpeakerName: "批評家", Content: "b"},
	}
	got := toTurns(wires)
	if len(got) != 2 || got[0].Content != "a" || got[1].SpeakerName != "批評家" {
		t.Errorf("toTurns = %+v, want order preserved", got)
	}
}
