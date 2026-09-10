package retrieval

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRerankerSendsNoPrefix(t *testing.T) {
	var raw []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		raw = buf
		_ = json.NewEncoder(w).Encode([]rerankResponseItem{{Index: 0, Score: 0.9}})
	}))
	t.Cleanup(srv.Close)

	r := NewReranker(srv.URL)
	if _, err := r.Rerank(context.Background(), "q", []string{"a"}); err != nil {
		t.Fatalf("Rerank: %v", err)
	}
	if strings.Contains(string(raw), "検索") {
		t.Errorf("request body contains a ruri bi-encoder prefix, want none: %s", raw)
	}
}

func TestRerankerPreservesOriginalIndex(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// TEI returns results sorted by score, not input order.
		_ = json.NewEncoder(w).Encode([]rerankResponseItem{
			{Index: 2, Score: 0.9},
			{Index: 0, Score: 0.5},
			{Index: 1, Score: 0.1},
		})
	}))
	t.Cleanup(srv.Close)

	r := NewReranker(srv.URL)
	got, err := r.Rerank(context.Background(), "q", []string{"a", "b", "c"})
	if err != nil {
		t.Fatalf("Rerank: %v", err)
	}
	want := []RerankResult{{Index: 2, Score: 0.9}, {Index: 0, Score: 0.5}, {Index: 1, Score: 0.1}}
	if len(got) != len(want) {
		t.Fatalf("got %d results, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("result[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}
