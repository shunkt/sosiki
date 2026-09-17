package meeting

import (
	"context"
	"fmt"
	"io"
	"iter"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/google/uuid"

	"github.com/shun/kaigi/backend/internal/a2aconv"
)

// call records one participant invocation: which persona, which round, and
// — critically — the transcript payload it actually received. This is what
// TestModeratorRounds inspects to verify personas can see each other's
// statements.
type call struct {
	slug    string
	round   int
	payload a2aconv.TranscriptPayload
}

// fakeDialer scripts each persona's reply text and records every call it
// receives, in order, so tests can assert both WHAT was sent (the
// transcript) and in WHAT ORDER participants were invoked.
type fakeDialer struct {
	mu      sync.Mutex
	calls   []call
	scripts map[string]string // slug -> reply text
	failOn  map[string]bool   // slug -> whether Dial should fail
}

func (f *fakeDialer) Dial(_ context.Context, p Participant) (agentClient, error) {
	if f.failOn[p.Slug] {
		return nil, fmt.Errorf("dial failed for %s", p.Slug)
	}
	return &fakeAgentClient{slug: p.Slug, dialer: f, reply: f.scripts[p.Slug]}, nil
}

type fakeAgentClient struct {
	slug   string
	dialer *fakeDialer
	reply  string
}

func (f *fakeAgentClient) SendStreamingMessage(_ context.Context, req *a2a.SendMessageRequest) iter.Seq2[a2a.Event, error] {
	payload, _, _ := a2aconv.TranscriptFrom(req.Message)

	f.dialer.mu.Lock()
	f.dialer.calls = append(f.dialer.calls, call{slug: f.slug, round: payload.Round, payload: payload})
	f.dialer.mu.Unlock()

	return func(yield func(a2a.Event, error) bool) {
		taskID := a2a.TaskID(f.slug + "-task")
		if !yield(&a2a.Task{ID: taskID, ContextID: "ctx", Status: a2a.TaskStatus{State: a2a.TaskStateSubmitted}}, nil) {
			return
		}
		if !yield(&a2a.TaskStatusUpdateEvent{TaskID: taskID, ContextID: "ctx", Status: a2a.TaskStatus{State: a2a.TaskStateWorking}}, nil) {
			return
		}
		artifact := &a2a.TaskArtifactUpdateEvent{
			TaskID: taskID, ContextID: "ctx",
			Artifact: &a2a.Artifact{ID: a2a.NewArtifactID(), Parts: a2a.ContentParts{a2a.NewTextPart(f.reply)}},
		}
		if !yield(artifact, nil) {
			return
		}
		yield(&a2a.TaskStatusUpdateEvent{TaskID: taskID, ContextID: "ctx", Status: a2a.TaskStatus{State: a2a.TaskStateCompleted}}, nil)
	}
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func twoParticipants() []Participant {
	return []Participant{
		{Slug: "critic", Name: "批評家"},
		{Slug: "pragmatist", Name: "実務家"},
	}
}

// TestModeratorRounds is the plan's stated acceptance condition for this
// entire feature: a later participant's transcript must include an earlier
// participant's statement — in the SAME round, not just prior rounds. If
// this fails, personas are answering independently, not conversing.
func TestModeratorRounds(t *testing.T) {
	pool := testPool(t)
	store := NewStore(pool)
	ctx := context.Background()

	id, err := store.Create(ctx, "合意形成について", twoParticipants())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	dialer := &fakeDialer{scripts: map[string]string{
		"critic":     "批評家の意見です",
		"pragmatist": "実務家の意見です",
	}}
	mod := NewModerator(store, dialer, testLogger(), 3)

	out := make(chan Event, 64)
	go func() {
		defer close(out)
		if err := mod.Run(ctx, id, "議題です", 2, out); err != nil {
			t.Errorf("Run: %v", err)
		}
	}()
	for range out {
	}

	dialer.mu.Lock()
	calls := append([]call(nil), dialer.calls...)
	dialer.mu.Unlock()

	if len(calls) != 4 {
		t.Fatalf("got %d calls, want 4 (2 participants x 2 rounds): %+v", len(calls), calls)
	}

	// Round 1, pragmatist (calls[1]): must see critic's round-1 statement,
	// made moments earlier in the SAME round.
	round1Pragmatist := calls[1]
	if round1Pragmatist.slug != "pragmatist" || round1Pragmatist.round != 1 {
		t.Fatalf("calls[1] = %+v, want pragmatist round 1", round1Pragmatist)
	}
	if !containsSpeakerContent(round1Pragmatist.payload.Transcript, "批評家", "批評家の意見です") {
		t.Errorf("round-1 pragmatist transcript does not include critic's round-1 statement: %+v",
			round1Pragmatist.payload.Transcript)
	}

	// Round 2, critic (calls[2]): must see BOTH round-1 statements.
	round2Critic := calls[2]
	if round2Critic.slug != "critic" || round2Critic.round != 2 {
		t.Fatalf("calls[2] = %+v, want critic round 2", round2Critic)
	}
	if !containsSpeakerContent(round2Critic.payload.Transcript, "批評家", "批評家の意見です") {
		t.Errorf("round-2 critic transcript does not include its own round-1 statement: %+v",
			round2Critic.payload.Transcript)
	}
	if !containsSpeakerContent(round2Critic.payload.Transcript, "実務家", "実務家の意見です") {
		t.Errorf("round-2 critic transcript does not include pragmatist's round-1 statement: %+v",
			round2Critic.payload.Transcript)
	}
}

func containsSpeakerContent(transcript []a2aconv.TurnWire, speaker, content string) bool {
	for _, t := range transcript {
		if t.SpeakerName == speaker && t.Content == content {
			return true
		}
	}
	return false
}

// TestModeratorSequential guards against a regression where the participant
// loop is parallelized (e.g. with errgroup, mirroring expandContext's
// pattern elsewhere in this codebase) — which would break the property
// TestModeratorRounds checks, since every participant in a round would then
// see the same stale transcript snapshot.
func TestModeratorSequential(t *testing.T) {
	pool := testPool(t)
	store := NewStore(pool)
	ctx := context.Background()

	id, err := store.Create(ctx, "topic", twoParticipants())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	dialer := &fakeDialer{scripts: map[string]string{"critic": "a", "pragmatist": "b"}}
	mod := NewModerator(store, dialer, testLogger(), 3)

	out := make(chan Event, 64)
	go func() {
		defer close(out)
		_ = mod.Run(ctx, id, "x", 2, out)
	}()
	for range out {
	}

	dialer.mu.Lock()
	defer dialer.mu.Unlock()
	want := []string{"critic", "pragmatist", "critic", "pragmatist"}
	if len(dialer.calls) != len(want) {
		t.Fatalf("got %d calls, want %d", len(dialer.calls), len(want))
	}
	for i, w := range want {
		if dialer.calls[i].slug != w {
			t.Errorf("calls[%d].slug = %q, want %q (call order = %v)", i, dialer.calls[i].slug, w, callSlugs(dialer.calls))
		}
	}
}

func callSlugs(calls []call) []string {
	out := make([]string, len(calls))
	for i, c := range calls {
		out[i] = c.slug
	}
	return out
}

// TestModeratorParticipantFailureContinues verifies a partial failure (one
// persona's Dial fails) does not abort the meeting — the other participant
// still gets to speak, and Run returns nil.
func TestModeratorParticipantFailureContinues(t *testing.T) {
	pool := testPool(t)
	store := NewStore(pool)
	ctx := context.Background()

	id, err := store.Create(ctx, "topic", twoParticipants())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	dialer := &fakeDialer{
		scripts: map[string]string{"pragmatist": "実務家の意見です"},
		failOn:  map[string]bool{"critic": true},
	}
	mod := NewModerator(store, dialer, testLogger(), 3)

	out := make(chan Event, 64)
	var events []Event
	go func() {
		defer close(out)
		if err := mod.Run(ctx, id, "x", 1, out); err != nil {
			t.Errorf("Run returned error for a partial failure: %v", err)
		}
	}()
	for ev := range out {
		events = append(events, ev)
	}

	var gotSpeakerError, gotSpeakerEndForPragmatist bool
	for _, ev := range events {
		if ev.Type == "speaker_error" && ev.PersonaSlug == "critic" {
			gotSpeakerError = true
		}
		if ev.Type == "speaker_end" && ev.PersonaSlug == "pragmatist" {
			gotSpeakerEndForPragmatist = true
		}
	}
	if !gotSpeakerError {
		t.Error("expected a speaker_error event for critic")
	}
	if !gotSpeakerEndForPragmatist {
		t.Error("expected pragmatist to still complete its turn")
	}
}

func TestModeratorClampsRounds(t *testing.T) {
	pool := testPool(t)
	store := NewStore(pool)
	ctx := context.Background()

	id, err := store.Create(ctx, "topic", twoParticipants())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	dialer := &fakeDialer{scripts: map[string]string{"critic": "a", "pragmatist": "b"}}
	mod := NewModerator(store, dialer, testLogger(), 2) // maxRounds = 2

	out := make(chan Event, 64)
	go func() {
		defer close(out)
		_ = mod.Run(ctx, id, "x", 99, out) // requests 99 rounds
	}()
	for range out {
	}

	dialer.mu.Lock()
	defer dialer.mu.Unlock()
	if len(dialer.calls) != 4 { // 2 participants x 2 (clamped) rounds
		t.Errorf("got %d calls, want 4 (clamped to maxRounds=2)", len(dialer.calls))
	}
}

// TestModeratorNoParticipants exercises Run's own defensive guard. Store.Create
// already refuses to create a participant-less meeting (see
// TestCreateRejectsNoParticipants in store_test.go), so reaching this path
// through Run requires bypassing Create and inserting the row directly —
// this is deliberately testing defense in depth, not a reachable API path.
func TestModeratorNoParticipants(t *testing.T) {
	pool := testPool(t)
	store := NewStore(pool)
	ctx := context.Background()

	var id string
	if err := pool.QueryRow(ctx, `INSERT INTO meetings (topic) VALUES ($1) RETURNING id`, "topic").Scan(&id); err != nil {
		t.Fatalf("insert meeting: %v", err)
	}

	dialer := &fakeDialer{}
	mod := NewModerator(store, dialer, testLogger(), 3)

	meetingID, err := uuid.Parse(id)
	if err != nil {
		t.Fatalf("parse meeting id: %v", err)
	}

	out := make(chan Event, 8)
	err = mod.Run(ctx, meetingID, "x", 1, out)
	close(out)
	if err != ErrNoParticipants {
		t.Errorf("Run error = %v, want ErrNoParticipants", err)
	}
}

// TestRunReturnsPromptlyWhenConsumerStopsReading is the regression test for
// the code-review finding: every out<-Event inside Run/runOneTurn used to be
// a plain blocking send with no select on ctx, so a client that disconnects
// mid-meeting (internal/api/turns.go's SSE handler returning on
// r.Context().Done() without draining events) could leave this goroutine
// parked forever the moment the channel's buffer filled — ctx cancellation
// alone does not unblock an in-flight channel send. Uses an UNBUFFERED out
// with no reader at all, so even the very first event (speaker_start) would
// deadlock without the fix.
func TestRunReturnsPromptlyWhenConsumerStopsReading(t *testing.T) {
	pool := testPool(t)
	store := NewStore(pool)
	ctx, cancel := context.WithCancel(context.Background())

	id, err := store.Create(context.Background(), "topic", twoParticipants())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	dialer := &fakeDialer{scripts: map[string]string{"critic": "a", "pragmatist": "b"}}
	mod := NewModerator(store, dialer, testLogger(), 3)

	out := make(chan Event) // unbuffered, and nothing ever reads from it
	done := make(chan error, 1)
	go func() { done <- mod.Run(ctx, id, "x", 2, out) }()

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Error("Run returned nil error after ctx cancellation, want ctx.Err()")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after ctx was canceled — goroutine leaked (regression)")
	}
}
