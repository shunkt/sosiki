package a2aconv

import (
	"encoding/json"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
)

func testPayload() TranscriptPayload {
	return TranscriptPayload{
		MeetingID:    "m-1",
		Round:        2,
		Participants: []string{"批評家", "実務家"},
		Transcript: []TurnWire{
			{Role: "user", SpeakerName: "user", Content: "議題"},
			{Role: "persona", SpeakerName: "批評家", Content: "批評家の発言"},
		},
	}
}

func TestNewRequestMessageAndTranscriptFromRoundTripSameProcess(t *testing.T) {
	payload := testPayload()
	msg := NewRequestMessage(payload, "続きをどうぞ")

	got, utterance, err := TranscriptFrom(msg)
	if err != nil {
		t.Fatalf("TranscriptFrom: %v", err)
	}
	if utterance != "続きをどうぞ" {
		t.Errorf("utterance = %q, want %q", utterance, "続きをどうぞ")
	}
	if got.MeetingID != payload.MeetingID || got.Round != payload.Round {
		t.Errorf("payload = %+v, want %+v", got, payload)
	}
	if len(got.Transcript) != len(payload.Transcript) {
		t.Fatalf("transcript len = %d, want %d", len(got.Transcript), len(payload.Transcript))
	}
}

// TestTranscriptFromAfterJSONRoundTrip is the critical test: it simulates
// what actually happens over the wire, where a2a.Part.Data() comes back as
// map[string]any rather than a TranscriptPayload struct. A direct type
// assertion here would fail; remarshal must not.
func TestTranscriptFromAfterJSONRoundTrip(t *testing.T) {
	payload := testPayload()
	msg := NewRequestMessage(payload, "続きをどうぞ")

	// Simulate the JSON-RPC transport: marshal the message to JSON and
	// unmarshal it back, exactly as a real A2A call over HTTP would.
	wire, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal message: %v", err)
	}
	var roundTripped a2a.Message
	if err := json.Unmarshal(wire, &roundTripped); err != nil {
		t.Fatalf("unmarshal message: %v", err)
	}

	got, utterance, err := TranscriptFrom(&roundTripped)
	if err != nil {
		t.Fatalf("TranscriptFrom after JSON round-trip: %v", err)
	}
	if utterance != "続きをどうぞ" {
		t.Errorf("utterance = %q, want %q", utterance, "続きをどうぞ")
	}
	if got.MeetingID != payload.MeetingID {
		t.Errorf("MeetingID = %q, want %q", got.MeetingID, payload.MeetingID)
	}
	if got.Round != payload.Round {
		t.Errorf("Round = %d, want %d", got.Round, payload.Round)
	}
	if len(got.Participants) != 2 || got.Participants[0] != "批評家" {
		t.Errorf("Participants = %v, want [批評家 実務家]", got.Participants)
	}
	if len(got.Transcript) != 2 || got.Transcript[1].Content != "批評家の発言" {
		t.Errorf("Transcript = %+v, want 2 turns with the second being 批評家の発言", got.Transcript)
	}
}

// TestTranscriptFromMissingDataPart covers a meeting's opening turn: no
// transcript DataPart exists yet, and that must not be treated as an error.
func TestTranscriptFromMissingDataPart(t *testing.T) {
	msg := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("最初の議題"))

	got, utterance, err := TranscriptFrom(msg)
	if err != nil {
		t.Fatalf("TranscriptFrom: %v", err)
	}
	if utterance != "最初の議題" {
		t.Errorf("utterance = %q, want %q", utterance, "最初の議題")
	}
	if got.MeetingID != "" || len(got.Transcript) != 0 {
		t.Errorf("payload = %+v, want zero value", got)
	}
}

func TestNewCitationsPartAndCitationsFromRoundTrip(t *testing.T) {
	cs := []CitationWire{
		{ChunkID: "c1", DocumentID: "d1", Title: "raft-paper.md", Relevance: 0.9, Affinity: 0.3},
	}
	part := NewCitationsPart(cs)

	got, ok, err := CitationsFrom(part)
	if err != nil {
		t.Fatalf("CitationsFrom: %v", err)
	}
	if !ok {
		t.Fatal("CitationsFrom ok = false, want true")
	}
	if len(got.Citations) != 1 || got.Citations[0].Title != "raft-paper.md" {
		t.Errorf("Citations = %+v, want 1 citation titled raft-paper.md", got.Citations)
	}
}

// TestCitationsFromAfterJSONRoundTrip mirrors
// TestTranscriptFromAfterJSONRoundTrip for the reverse direction (persona ->
// moderator).
func TestCitationsFromAfterJSONRoundTrip(t *testing.T) {
	cs := []CitationWire{{ChunkID: "c1", Title: "raft-paper.md", Relevance: 0.9}}
	part := NewCitationsPart(cs)

	wire, err := json.Marshal(part)
	if err != nil {
		t.Fatalf("marshal part: %v", err)
	}
	var roundTripped a2a.Part
	if err := json.Unmarshal(wire, &roundTripped); err != nil {
		t.Fatalf("unmarshal part: %v", err)
	}

	got, ok, err := CitationsFrom(&roundTripped)
	if err != nil {
		t.Fatalf("CitationsFrom after JSON round-trip: %v", err)
	}
	if !ok {
		t.Fatal("CitationsFrom ok = false, want true")
	}
	if len(got.Citations) != 1 || got.Citations[0].Title != "raft-paper.md" {
		t.Errorf("Citations = %+v, want 1 citation titled raft-paper.md", got.Citations)
	}
}

func TestCitationsFromSkipsNonCitationPart(t *testing.T) {
	part := a2a.NewTextPart("plain text, not a citation")
	_, ok, err := CitationsFrom(part)
	if err != nil {
		t.Fatalf("CitationsFrom: %v", err)
	}
	if ok {
		t.Error("CitationsFrom ok = true for a plain text part, want false")
	}
}
