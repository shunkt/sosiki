// Package retrieval turns a user utterance plus a persona into ranked knowledge.
package retrieval

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// ruri-v3 is prefix-conditioned: the same text embedded under a different
// prefix lands somewhere else in the space. Getting these wrong degrades
// recall silently, so no caller outside this file writes a prefix.
const (
	prefixQuery    = "検索クエリ: "
	prefixDocument = "検索文書: "
)

// Dimensions is ruri-v3-310m's output size and must match vector(768) in SQL.
const Dimensions = 768

// maxBatch keeps each /embed request under TEI's per-request token budget.
const maxBatch = 32

type Embedder struct {
	baseURL string
	http    *http.Client
}

func NewEmbedder(baseURL string) *Embedder {
	return &Embedder{
		baseURL: baseURL,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

// EmbedQueries embeds retrieval queries. Persona interest topics also go
// through here, not through a "トピック: " prefix: affinity is scored against
// document chunks, and query-to-document is the pairing ruri was trained on.
func (e *Embedder) EmbedQueries(ctx context.Context, texts []string) ([][]float32, error) {
	return e.embed(ctx, prefixQuery, texts)
}

// EmbedDocuments embeds passages destined for the chunks table.
func (e *Embedder) EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error) {
	return e.embed(ctx, prefixDocument, texts)
}

type embedRequest struct {
	Inputs    []string `json:"inputs"`
	Normalize bool     `json:"normalize"`
	Truncate  bool     `json:"truncate"`
}

func (e *Embedder) embed(ctx context.Context, prefix string, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	out := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += maxBatch {
		end := min(start+maxBatch, len(texts))
		batch, err := e.embedBatch(ctx, prefix, texts[start:end])
		if err != nil {
			return nil, err
		}
		out = append(out, batch...)
	}
	return out, nil
}

func (e *Embedder) embedBatch(ctx context.Context, prefix string, texts []string) ([][]float32, error) {
	inputs := make([]string, len(texts))
	for i, t := range texts {
		inputs[i] = prefix + t
	}

	body, err := json.Marshal(embedRequest{Inputs: inputs, Normalize: true, Truncate: true})
	if err != nil {
		return nil, fmt.Errorf("embed: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.baseURL+"/embed", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("embed: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embed: request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embed: TEI returned %s", resp.Status)
	}

	var vectors [][]float32
	if err := json.NewDecoder(resp.Body).Decode(&vectors); err != nil {
		return nil, fmt.Errorf("embed: decode response: %w", err)
	}
	for i, v := range vectors {
		if len(v) != Dimensions {
			return nil, fmt.Errorf("embed: vector %d has %d dimensions, want %d", i, len(v), Dimensions)
		}
	}
	return vectors, nil
}
