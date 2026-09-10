package retrieval

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func fakeEmbedServer(t *testing.T, dims int) (*httptest.Server, *embedRequest) {
	t.Helper()
	captured := &embedRequest{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		vectors := make([][]float32, len(captured.Inputs))
		for i := range vectors {
			vectors[i] = make([]float32, dims)
		}
		_ = json.NewEncoder(w).Encode(vectors)
	}))
	t.Cleanup(srv.Close)
	return srv, captured
}

func TestEmbedderAppliesQueryPrefix(t *testing.T) {
	srv, captured := fakeEmbedServer(t, Dimensions)
	e := NewEmbedder(srv.URL)

	if _, err := e.EmbedQueries(context.Background(), []string{"合意形成"}); err != nil {
		t.Fatalf("EmbedQueries: %v", err)
	}
	if want := []string{"検索クエリ: 合意形成"}; !equalStrings(captured.Inputs, want) {
		t.Errorf("inputs = %v, want %v", captured.Inputs, want)
	}
	if !captured.Normalize {
		t.Error("normalize = false, want true (required for cosine)")
	}
}

func TestEmbedderAppliesDocumentPrefix(t *testing.T) {
	srv, captured := fakeEmbedServer(t, Dimensions)
	e := NewEmbedder(srv.URL)

	if _, err := e.EmbedDocuments(context.Background(), []string{"本文"}); err != nil {
		t.Fatalf("EmbedDocuments: %v", err)
	}
	if want := []string{"検索文書: 本文"}; !equalStrings(captured.Inputs, want) {
		t.Errorf("inputs = %v, want %v", captured.Inputs, want)
	}
}

func TestEmbedderRejectsWrongDimensions(t *testing.T) {
	srv, _ := fakeEmbedServer(t, 512)
	e := NewEmbedder(srv.URL)

	_, err := e.EmbedQueries(context.Background(), []string{"x"})
	if err == nil {
		t.Fatal("expected an error for a 512-dimension response, got nil")
	}
	if !strings.Contains(err.Error(), "512") {
		t.Errorf("error %q does not mention the wrong dimension count", err)
	}
}

func TestEmbedderBatchesLargeInputs(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var req embedRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		vectors := make([][]float32, len(req.Inputs))
		for i := range vectors {
			vectors[i] = make([]float32, Dimensions)
		}
		_ = json.NewEncoder(w).Encode(vectors)
	}))
	t.Cleanup(srv.Close)

	e := NewEmbedder(srv.URL)
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
