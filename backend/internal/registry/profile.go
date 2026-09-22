package registry

import (
	"encoding/json"
	"sort"

	"github.com/a2aproject/a2a-go/v2/a2a"
)

// ProfileExtensionURI identifies the A2A card extension a persona pod uses
// to publish its own personality profile — see a2aconv.PersonaCard, which is
// the only writer of this extension, and ProfileFromCard, its only reader.
const ProfileExtensionURI = "https://kaigi.local/ext/persona-profile/v1"

// ProfileInterest is one topic a persona is biased toward (positive Weight)
// or away from (negative Weight) — the API-facing counterpart of
// persona.Interest, minus its Embedding (never meant to leave the backend).
type ProfileInterest struct {
	Topic  string  `json:"topic"`
	Weight float32 `json:"weight"`
}

// Profile is a persona's personality, as carried on its A2A AgentCard for
// display in the persona directory (see the plan's UX Design). It is the
// API-facing counterpart of persona.Personality, minus Interest.Embedding.
type Profile struct {
	Stance     string            `json:"stance"`
	Skepticism float32           `json:"skepticism"`
	Verbosity  string            `json:"verbosity"`
	Interests  []ProfileInterest `json:"interests"`
}

// ProfileFromCard extracts the persona profile extension from a card, if
// present. It returns nil rather than an error for every failure case
// (extension absent, params malformed) because a profile is optional,
// display-only data — a persona pod that has not yet re-registered with the
// extension, or a card from an unrelated agent, must not break the persona
// directory, only omit its detail.
func ProfileFromCard(card *a2a.AgentCard) *Profile {
	if card == nil {
		return nil
	}
	for _, ext := range card.Capabilities.Extensions {
		if ext.URI != ProfileExtensionURI {
			continue
		}
		// Params is a map[string]any (arbitrary JSON in Go's decoded shape,
		// e.g. numbers as float64) rather than *Profile itself, so this
		// round-trips it through JSON to land on Profile's own types
		// instead of hand-walking the map.
		raw, err := json.Marshal(ext.Params)
		if err != nil {
			return nil
		}
		var p Profile
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil
		}
		return &p
	}
	return nil
}

// profileParams converts a Profile into the map[string]any shape
// AgentExtension.Params expects, sorting Interests by Weight descending
// (Topic ascending on ties) so the resulting card is byte-stable across
// restarts — interests come back from Postgres in no guaranteed order, the
// same reasoning a2aconv.PersonaCard already applies to its skill tags.
func profileParams(p Profile) (map[string]any, error) {
	sorted := make([]ProfileInterest, len(p.Interests))
	copy(sorted, p.Interests)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Weight != sorted[j].Weight {
			return sorted[i].Weight > sorted[j].Weight
		}
		return sorted[i].Topic < sorted[j].Topic
	})
	p.Interests = sorted

	raw, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	var params map[string]any
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, err
	}
	return params, nil
}

// ProfileExtension builds the AgentExtension a2aconv.PersonaCard attaches to
// a persona's card. It lives here (not in a2aconv) so the extension's shape
// and its reader (ProfileFromCard) never drift apart.
func ProfileExtension(p Profile) (a2a.AgentExtension, error) {
	params, err := profileParams(p)
	if err != nil {
		return a2a.AgentExtension{}, err
	}
	return a2a.AgentExtension{
		URI:         ProfileExtensionURI,
		Description: "persona personality profile",
		Params:      params,
	}, nil
}
