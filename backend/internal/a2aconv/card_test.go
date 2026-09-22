package a2aconv

import (
	"testing"

	"github.com/shun/kaigi/backend/internal/persona"
	"github.com/shun/kaigi/backend/internal/registry"
)

func testCardPersona() persona.Persona {
	return persona.Persona{
		Name: "批評家",
		Personality: persona.Personality{
			Stance: "根拠のない主張には懐疑的",
			Interests: []persona.Interest{
				{Topic: "マーケティング", Weight: -0.5},
				{Topic: "形式的検証", Weight: 0.8},
				{Topic: "Raft", Weight: 0.6},
			},
			Skepticism: 0.85,
		},
	}
}

// TestPersonaCardSortsTags guards the card's byte-stability: interests come
// back from Postgres in no guaranteed order, but the card must render the
// same tag list every time regardless.
func TestPersonaCardSortsTags(t *testing.T) {
	card := PersonaCard(testCardPersona(), "http://persona-critic:8082")
	tags := card.Skills[0].Tags
	if len(tags) != 2 {
		t.Fatalf("tags = %v, want 2 (only positive-weight interests)", tags)
	}
	if tags[0] != "Raft" || tags[1] != "形式的検証" {
		t.Errorf("tags = %v, want sorted [Raft 形式的検証]", tags)
	}
}

func TestPersonaCardExcludesNegativeWeightInterests(t *testing.T) {
	card := PersonaCard(testCardPersona(), "http://persona-critic:8082")
	for _, tag := range card.Skills[0].Tags {
		if tag == "マーケティング" {
			t.Errorf("tags include negative-weight interest %q", tag)
		}
	}
}

// TestPersonaCardProtocolVersion guards against constructing AgentInterface
// as a struct literal instead of via a2a.NewAgentInterface, which is what
// stamps ProtocolVersion — see the plan's GOTCHA.
func TestPersonaCardProtocolVersion(t *testing.T) {
	card := PersonaCard(testCardPersona(), "http://persona-critic:8082")
	if len(card.SupportedInterfaces) != 1 {
		t.Fatalf("SupportedInterfaces = %v, want 1 entry", card.SupportedInterfaces)
	}
	iface := card.SupportedInterfaces[0]
	if iface.ProtocolVersion == "" {
		t.Error("ProtocolVersion is empty, want a2a.Version (use a2a.NewAgentInterface, not a struct literal)")
	}
	if iface.URL != "http://persona-critic:8082" {
		t.Errorf("URL = %q, want the publicURL passed in", iface.URL)
	}
}

func TestPersonaCardStreamingCapability(t *testing.T) {
	card := PersonaCard(testCardPersona(), "http://persona-critic:8082")
	if !card.Capabilities.Streaming {
		t.Error("Capabilities.Streaming = false, want true (persona pods stream tokens)")
	}
	if card.Capabilities.PushNotifications {
		t.Error("Capabilities.PushNotifications = true, want false (out of scope — see plan's NOT Building)")
	}
}

// TestPersonaCardCarriesProfileExtension guards the new profile channel:
// registry.ProfileFromCard must recover the full personality — including
// the negative-weight interest that Skills[0].Tags deliberately excludes.
func TestPersonaCardCarriesProfileExtension(t *testing.T) {
	p := testCardPersona()
	card := PersonaCard(p, "http://persona-critic:8082")

	profile := registry.ProfileFromCard(card)
	if profile == nil {
		t.Fatal("registry.ProfileFromCard(card) = nil, want a profile")
	}
	if profile.Stance != p.Personality.Stance {
		t.Errorf("Stance = %q, want %q", profile.Stance, p.Personality.Stance)
	}
	if profile.Skepticism != p.Personality.Skepticism {
		t.Errorf("Skepticism = %v, want %v", profile.Skepticism, p.Personality.Skepticism)
	}
	if len(profile.Interests) != 3 {
		t.Fatalf("Interests = %v, want all 3 interests (including negative-weight)", profile.Interests)
	}
	// Weight descending, Topic ascending on ties: 形式的検証(0.8), Raft(0.6), マーケティング(-0.5).
	wantOrder := []string{"形式的検証", "Raft", "マーケティング"}
	for i, topic := range wantOrder {
		if profile.Interests[i].Topic != topic {
			t.Errorf("Interests[%d].Topic = %q, want %q (order = %v)", i, profile.Interests[i].Topic, topic, profile.Interests)
		}
	}
	if profile.Interests[2].Weight != -0.5 {
		t.Errorf("Interests[2].Weight = %v, want -0.5 (negative-weight interest missing from profile)", profile.Interests[2].Weight)
	}
}

func TestPersonaCardIsByteStableAcrossCalls(t *testing.T) {
	p := testCardPersona()
	a := PersonaCard(p, "http://persona-critic:8082")
	b := PersonaCard(p, "http://persona-critic:8082")
	if a.Name != b.Name || len(a.Skills[0].Tags) != len(b.Skills[0].Tags) {
		t.Error("PersonaCard is not stable across calls with the same persona")
	}
	for i := range a.Skills[0].Tags {
		if a.Skills[0].Tags[i] != b.Skills[0].Tags[i] {
			t.Errorf("tag order differs: %v vs %v", a.Skills[0].Tags, b.Skills[0].Tags)
		}
	}
}
