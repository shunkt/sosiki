package retrieval

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type Reranker struct {
	baseURL string
	http    *http.Client
}

func NewReranker(baseURL string) *Reranker {
	return &Reranker{
		baseURL: baseURL,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

type RerankResult struct {
	Index int     // index into the texts slice passed to Rerank
	Score float32 // relevance in roughly 0..1
}

type rerankRequest struct {
	Query     string   `json:"query"`
	Texts     []string `json:"texts"`
	RawScores bool     `json:"raw_scores"`
	Truncate  bool     `json:"truncate"`
}

type rerankResponseItem struct {
	Index int     `json:"index"`
	Score float32 `json:"score"`
}

// Rerank scores each text against the query with a cross-encoder. Unlike the
// bi-encoder in embed.go, ruri's reranker takes raw pairs — no prefixes.
func (r *Reranker) Rerank(ctx context.Context, query string, texts []string) ([]RerankResult, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	body, err := json.Marshal(rerankRequest{
		Query:     query,
		Texts:     texts,
		RawScores: false,
		Truncate:  true,
	})
	if err != nil {
		return nil, fmt.Errorf("rerank: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.baseURL+"/rerank", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("rerank: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("rerank: request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("rerank: TEI returned %s", resp.Status)
	}

	var items []rerankResponseItem
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		return nil, fmt.Errorf("rerank: decode response: %w", err)
	}

	// TEI returns results sorted by score; callers should not depend on that
	// order, so pass the original index through untouched.
	out := make([]RerankResult, len(items))
	for i, item := range items {
		out[i] = RerankResult{Index: item.Index, Score: item.Score}
	}
	return out, nil
}
