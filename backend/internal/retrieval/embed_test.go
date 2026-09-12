package retrieval

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	openai "github.com/sashabaranov/go-openai"

	"github.com/shun/kaigi/backend/internal/config"
)

type capturedEmbedRequest struct {
	Input      []string `json:"input"`
	Model      string   `json:"model"`
	Dimensions int      `json:"dimensions"`
}

// fakeEmbedServer stands in for OpenAI's /embeddings endpoint. It returns
// vectors out of input order, with explicit indexes, because that is the
// one property of the real API the client must not assume away.
func fakeEmbedServer(t *testing.T, dims int) (*httptest.Server, *capturedEmbedRequest) {
	t.Helper()
	captured := &capturedEmbedRequest{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		data := make([]openai.Embedding, len(captured.Input))
		for i := range data {
			// Reverse order, so a client that ignores Index and appends in
			// arrival order fails this test.
			idx := len(captured.Input) - 1 - i
			data[i] = openai.Embedding{
				Index:     idx,
				Embedding: marker(dims, idx),
			}
		}
		_ = json.NewEncoder(w).Encode(openai.EmbeddingResponse{Data: data})
	}))
	t.Cleanup(srv.Close)
	return srv, captured
}

// marker builds a vector whose first element identifies which input it
// belongs to, so tests can assert the client reassembled them in order.
func marker(dims, id int) []float32 {
	v := make([]float32, dims)
	if dims > 0 {
		v[0] = float32(id)
	}
	return v
}

func newTestEmbedder(baseURL string) *Embedder {
	return NewEmbedder(config.EmbedConfig{
		BaseURL: baseURL,
		APIKey:  "test-key",
		Model:   "text-embedding-3-small",
	})
}

func TestEmbedderSendsNoPrefix(t *testing.T) {
	srv, captured := fakeEmbedServer(t, Dimensions)
	e := newTestEmbedder(srv.URL)

	if _, err := e.EmbedQueries(context.Background(), []string{"合意形成"}); err != nil {
		t.Fatalf("EmbedQueries: %v", err)
	}
	if want := []string{"合意形成"}; !equalStrings(captured.Input, want) {
		t.Errorf("inputs = %v, want %v", captured.Input, want)
	}

	if _, err := e.EmbedDocuments(context.Background(), []string{"本文"}); err != nil {
		t.Fatalf("EmbedDocuments: %v", err)
	}
	if want := []string{"本文"}; !equalStrings(captured.Input, want) {
		t.Errorf("inputs = %v, want %v", captured.Input, want)
	}
}

func TestEmbedderRequestsConfiguredDimensions(t *testing.T) {
	srv, captured := fakeEmbedServer(t, Dimensions)
	e := newTestEmbedder(srv.URL)

	if _, err := e.EmbedQueries(context.Background(), []string{"x"}); err != nil {
		t.Fatalf("EmbedQueries: %v", err)
	}
	if captured.Dimensions != Dimensions {
		t.Errorf("dimensions = %d, want %d", captured.Dimensions, Dimensions)
	}
	if captured.Model != "text-embedding-3-small" {
		t.Errorf("model = %q, want %q", captured.Model, "text-embedding-3-small")
	}
}

func TestEmbedderRejectsWrongDimensions(t *testing.T) {
	srv, _ := fakeEmbedServer(t, 512)
	e := newTestEmbedder(srv.URL)

	_, err := e.EmbedQueries(context.Background(), []string{"x"})
	if err == nil {
		t.Fatal("expected an error for a 512-dimension response, got nil")
	}
	if !strings.Contains(err.Error(), "512") {
		t.Errorf("error %q does not mention the wrong dimension count", err)
	}
}

func TestEmbedderReordersByResponseIndex(t *testing.T) {
	srv, _ := fakeEmbedServer(t, Dimensions)
	e := newTestEmbedder(srv.URL)

	got, err := e.EmbedQueries(context.Background(), []string{"a", "b", "c"})
	if err != nil {
		t.Fatalf("EmbedQueries: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d vectors, want 3", len(got))
	}
	for i, v := range got {
		if v[0] != float32(i) {
			t.Errorf("vector %d marker = %v, want %v", i, v[0], float32(i))
		}
	}
}

func TestEmbedderBatchesLargeInputs(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var req capturedEmbedRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		data := make([]openai.Embedding, len(req.Input))
		for i := range data {
			data[i] = openai.Embedding{Index: i, Embedding: make([]float32, Dimensions)}
		}
		_ = json.NewEncoder(w).Encode(openai.EmbeddingResponse{Data: data})
	}))
	t.Cleanup(srv.Close)

	e := newTestEmbedder(srv.URL)
	texts := make([]string, maxBatch+1)
	for i := range texts {
		texts[i] = "text"
	}

	got, err := e.EmbedQueries(context.Background(), texts)
	if err != nil {
		t.Fatalf("EmbedQueries: %v", err)
	}
	if len(got) != len(texts) {
		t.Errorf("got %d vectors, want %d", len(got), len(texts))
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2 (batch split at %d)", calls, maxBatch)
	}
}

func TestEmbedderSubstitutesEmptyInput(t *testing.T) {
	srv, captured := fakeEmbedServer(t, Dimensions)
	e := newTestEmbedder(srv.URL)

	if _, err := e.EmbedQueries(context.Background(), []string{"", "x"}); err != nil {
		t.Fatalf("EmbedQueries: %v", err)
	}
	if len(captured.Input) != 2 || captured.Input[0] != " " {
		t.Errorf("inputs = %v, want first element to be a single space", captured.Input)
	}
}

func TestEmbedderEmptyInputSlice(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
	}))
	t.Cleanup(srv.Close)

	e := newTestEmbedder(srv.URL)
	got, err := e.EmbedQueries(context.Background(), nil)
	if err != nil {
		t.Fatalf("EmbedQueries: %v", err)
	}
	if got != nil {
		t.Errorf("got %v, want nil for empty input", got)
	}
	if calls != 0 {
		t.Errorf("calls = %d, want 0", calls)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
