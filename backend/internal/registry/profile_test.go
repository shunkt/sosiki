package registry

import (
	"encoding/json"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
)

func testProfile() Profile {
	return Profile{
		Stance:     "根拠のない主張には懐疑的",
		Skepticism: 0.85,
		Verbosity:  "concise",
		Interests: []ProfileInterest{
			{Topic: "マーケティング", Weight: -0.5},
			{Topic: "形式的検証", Weight: 0.8},
			{Topic: "Raft", Weight: 0.6},
		},
	}
}

func TestProfileFromCardNilCard(t *testing.T) {
	if got := ProfileFromCard(nil); got != nil {
		t.Errorf("ProfileFromCard(nil) = %v, want nil", got)
	}
}

func TestProfileFromCardMissingExtension(t *testing.T) {
	card := &a2a.AgentCard{Capabilities: a2a.AgentCapabilities{}}
	if got := ProfileFromCard(card); got != nil {
		t.Errorf("ProfileFromCard(no extensions) = %v, want nil", got)
	}
}

func TestProfileFromCardMalformedParams(t *testing.T) {
	card := &a2a.AgentCard{
		Capabilities: a2a.AgentCapabilities{
			Extensions: []a2a.AgentExtension{{
				URI: ProfileExtensionURI,
				// skepticism should be a number; a string must not panic,
				// just fail to decode into Profile and yield nil.
				Params: map[string]any{"skepticism": "not-a-number"},
			}},
		},
	}
	if got := ProfileFromCard(card); got != nil {
		t.Errorf("ProfileFromCard(malformed params) = %v, want nil", got)
	}
}

// TestProfileRoundTripsThroughCard guards the whole path ProfileExtension
// writes and ProfileFromCard reads, including the extra JSON round trip
// AgentExtension.Params (map[string]any) forces on every field.
func TestProfileRoundTripsThroughCard(t *testing.T) {
	want := testProfile()
	ext, err := ProfileExtension(want)
	if err != nil {
		t.Fatalf("ProfileExtension: %v", err)
	}
	card := &a2a.AgentCard{
		Capabilities: a2a.AgentCapabilities{Extensions: []a2a.AgentExtension{ext}},
	}

	got := ProfileFromCard(card)
	if got == nil {
		t.Fatal("ProfileFromCard = nil, want a profile")
	}
	if got.Stance != want.Stance {
		t.Errorf("Stance = %q, want %q", got.Stance, want.Stance)
	}
	if got.Skepticism != want.Skepticism {
		t.Errorf("Skepticism = %v, want %v", got.Skepticism, want.Skepticism)
	}
	if got.Verbosity != want.Verbosity {
		t.Errorf("Verbosity = %q, want %q", got.Verbosity, want.Verbosity)
	}
	if len(got.Interests) != 3 {
		t.Fatalf("Interests = %v, want 3 entries", got.Interests)
	}
	// Weight descending, Topic ascending on ties: 形式的検証(0.8), Raft(0.6), マーケティング(-0.5).
	wantOrder := []string{"形式的検証", "Raft", "マーケティング"}
	for i, topic := range wantOrder {
		if got.Interests[i].Topic != topic {
			t.Errorf("Interests[%d].Topic = %q, want %q (order = %v)", i, got.Interests[i].Topic, topic, got.Interests)
		}
	}
	if got.Interests[2].Weight != -0.5 {
		t.Errorf("Interests[2].Weight = %v, want -0.5 (negative weight lost in round trip?)", got.Interests[2].Weight)
	}
}

// TestProfileSurvivesJSONCardRoundTrip guards the path registry.Store
// actually exercises: the whole AgentCard is marshaled to JSONB and back
// (see Store.Upsert/Get), so Params must survive a raw json.Marshal +
// json.Unmarshal into a2a.AgentCard, not just ProfileExtension's own call.
func TestProfileSurvivesJSONCardRoundTrip(t *testing.T) {
	want := testProfile()
	ext, err := ProfileExtension(want)
	if err != nil {
		t.Fatalf("ProfileExtension: %v", err)
	}
	card := &a2a.AgentCard{
		Capabilities: a2a.AgentCapabilities{Extensions: []a2a.AgentExtension{ext}},
	}

	raw, err := json.Marshal(card)
	if err != nil {
		t.Fatalf("json.Marshal(card): %v", err)
	}
	var decoded a2a.AgentCard
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("json.Unmarshal(card): %v", err)
	}

	got := ProfileFromCard(&decoded)
	if got == nil {
		t.Fatal("ProfileFromCard(decoded card) = nil, want a profile")
	}
	if got.Skepticism != want.Skepticism || len(got.Interests) != len(want.Interests) {
		t.Errorf("profile did not survive JSON round trip: got %+v, want %+v", got, want)
	}
}
