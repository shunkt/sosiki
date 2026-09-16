package agentapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/google/uuid"

	"github.com/shun/kaigi/backend/internal/registry"
)

// fakeResolver drives handleRegister's card-resolution step without a live
// persona pod.
type fakeResolver struct {
	card *a2a.AgentCard
	err  error
}

func (f fakeResolver) Resolve(context.Context, string) (*a2a.AgentCard, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.card, nil
}

func testDeps(resolver cardResolver) Deps {
	return Deps{
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Registry: nil, // set per-test where needed via a real store integration test
		Resolver: resolver,
		TTL:      45 * time.Second,
	}
}

func TestHandleHealthUnconfiguredWithoutPool(t *testing.T) {
	h := NewHandler(testDeps(fakeResolver{}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["status"] != "degraded" {
		t.Errorf("status = %q, want degraded", body["status"])
	}
}

func TestHandleRegisterRejectsMissingFields(t *testing.T) {
	h := NewHandler(testDeps(fakeResolver{}))

	body, _ := json.Marshal(registerRequest{Slug: "", BaseURL: ""})
	req := httptest.NewRequest(http.MethodPost, "/registry/agents", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestHandleRegisterRejectsInvalidPersonaID(t *testing.T) {
	h := NewHandler(testDeps(fakeResolver{}))

	body, _ := json.Marshal(registerRequest{Slug: "critic", BaseURL: "http://x:8082", PersonaID: "not-a-uuid"})
	req := httptest.NewRequest(http.MethodPost, "/registry/agents", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

// TestHandleRegisterRejectsUnresolvableCard covers the GOTCHA: a persona pod
// whose baseUrl doesn't actually serve an agent card must not be registered
// silently — the moderator would otherwise list it as present with an empty
// card.
func TestHandleRegisterRejectsUnresolvableCard(t *testing.T) {
	h := NewHandler(testDeps(fakeResolver{err: errors.New("connection refused")}))

	body, _ := json.Marshal(registerRequest{
		Slug: "critic", BaseURL: "http://unreachable:8082", PersonaID: uuid.NewString(),
	})
	req := httptest.NewRequest(http.MethodPost, "/registry/agents", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestToAgentDTOFlattensSkillTags(t *testing.T) {
	card := &a2a.AgentCard{
		Name: "批評家",
		Skills: []a2a.AgentSkill{
			{ID: "opinion", Name: "意見表明", Tags: []string{"Raft", "形式手法"}},
		},
	}
	dto := toAgentDTO(registry.Agent{Slug: "critic", Card: card}, true)

	if dto.Name != "批評家" {
		t.Errorf("Name = %q, want 批評家", dto.Name)
	}
	if len(dto.Skills) != 1 || len(dto.Skills[0].Tags) != 2 {
		t.Errorf("Skills = %+v, want 1 skill with 2 tags", dto.Skills)
	}
	if !dto.Present {
		t.Error("Present = false, want true")
	}
}
