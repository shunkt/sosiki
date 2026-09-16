package meeting

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
)

func TestRegistryCardSourceFetchesCard(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/registry/agents/critic" {
			t.Errorf("path = %q, want /registry/agents/critic", r.URL.Path)
		}
		card := &a2a.AgentCard{
			Name: "批評家",
			SupportedInterfaces: []*a2a.AgentInterface{
				a2a.NewAgentInterface("http://persona-critic:8082", a2a.TransportProtocolJSONRPC),
			},
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"slug": "critic", "card": card})
	}))
	defer srv.Close()

	src := NewRegistryCardSource(srv.URL)
	card, err := src.CardFor(context.Background(), "critic")
	if err != nil {
		t.Fatalf("CardFor: %v", err)
	}
	if card.Name != "批評家" {
		t.Errorf("Name = %q, want 批評家", card.Name)
	}
}

func TestRegistryCardSourceNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	src := NewRegistryCardSource(srv.URL)
	if _, err := src.CardFor(context.Background(), "does-not-exist"); err == nil {
		t.Error("CardFor for a 404 returned nil error")
	}
}

// fakeCardSource lets TestA2ADialerCachesClients count resolution calls
// without a live discovery pod.
type fakeCardSource struct {
	resolves int
	card     *a2a.AgentCard
}

func (f *fakeCardSource) CardFor(context.Context, string) (*a2a.AgentCard, error) {
	f.resolves++
	return f.card, nil
}

func TestA2ADialerCachesClients(t *testing.T) {
	// A real (unstarted) URL is fine here: NewFromCard only needs a
	// well-formed AgentInterface to select a transport, it does not dial
	// eagerly.
	cards := &fakeCardSource{card: &a2a.AgentCard{
		Name: "批評家",
		SupportedInterfaces: []*a2a.AgentInterface{
			a2a.NewAgentInterface("http://persona-critic:8082", a2a.TransportProtocolJSONRPC),
		},
	}}
	dialer := NewA2ADialer(cards)

	p := Participant{Slug: "critic", Name: "批評家"}
	if _, err := dialer.Dial(context.Background(), p); err != nil {
		t.Fatalf("first Dial: %v", err)
	}
	if _, err := dialer.Dial(context.Background(), p); err != nil {
		t.Fatalf("second Dial: %v", err)
	}

	if cards.resolves != 1 {
		t.Errorf("card resolved %d times, want 1 (second Dial should hit the cache)", cards.resolves)
	}

	dialer.Forget(p.Slug)
	if _, err := dialer.Dial(context.Background(), p); err != nil {
		t.Fatalf("Dial after Forget: %v", err)
	}
	if cards.resolves != 2 {
		t.Errorf("card resolved %d times after Forget, want 2", cards.resolves)
	}
}
