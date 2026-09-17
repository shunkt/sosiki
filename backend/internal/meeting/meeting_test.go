package meeting

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestEventJSONUsesCamelCaseKeys guards the exact inconsistency an actual
// captured SSE stream caught: meeting.Event is marshaled directly (no DTO
// layer, unlike the REST endpoints in internal/api), so Turn/Citation's own
// json tags are what the frontend actually receives. Without them, Go's
// default PascalCase field names would leak into the wire format and
// silently mismatch the REST API's camelCase.
func TestEventJSONUsesCamelCaseKeys(t *testing.T) {
	ev := Event{
		Type:        "speaker_end",
		PersonaSlug: "critic",
		Turn: &Turn{
			ID:          uuid.New(),
			Seq:         1,
			Round:       1,
			Role:        "persona",
			SpeakerSlug: "critic",
			SpeakerName: "批評家",
			Content:     "発言",
			CreatedAt:   time.Now(),
			Citations: []Citation{
				{ChunkID: uuid.New(), DocumentID: uuid.New(), Title: "doc"},
			},
		},
	}

	body, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	turn, ok := decoded["turn"].(map[string]any)
	if !ok {
		t.Fatalf("decoded[\"turn\"] = %T, want map[string]any (wrong key case would make this assertion fail too)", decoded["turn"])
	}
	for _, key := range []string{"id", "seq", "round", "role", "speakerSlug", "speakerName", "content", "createdAt", "citations"} {
		if _, ok := turn[key]; !ok {
			t.Errorf("turn JSON is missing camelCase key %q (got keys: %v)", key, keysOf(turn))
		}
	}

	citations, ok := turn["citations"].([]any)
	if !ok || len(citations) != 1 {
		t.Fatalf("turn[\"citations\"] = %v, want a 1-element array", turn["citations"])
	}
	citation, ok := citations[0].(map[string]any)
	if !ok {
		t.Fatalf("citations[0] = %T, want map[string]any", citations[0])
	}
	for _, key := range []string{"chunkId", "documentId", "title", "url", "relevance", "affinity"} {
		if _, ok := citation[key]; !ok {
			t.Errorf("citation JSON is missing camelCase key %q (got keys: %v)", key, keysOf(citation))
		}
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
