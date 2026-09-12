// Package retrieval turns a user utterance plus a persona into ranked knowledge.
package retrieval

import (
	"context"
	"fmt"
	"strings"

	openai "github.com/sashabaranov/go-openai"

	"github.com/shun/kaigi/backend/internal/config"
)

// Dimensions is text-embedding-3-small's native output size and must match
// vector(1536) in SQL. The request sends it explicitly rather than relying on
// the model default, so a model swap that changes the width fails loudly at
// the response check below instead of at the INSERT.
const Dimensions = 1536

// maxBatch keeps each request well inside OpenAI's per-request limits (2048
// inputs, 300k tokens). Seed chunks are ~800 runes, so 128 of them is roughly
// 150k tokens — comfortable, and small enough that one failure retries little.
const maxBatch = 128

type Embedder struct {
	client *openai.Client
	model  string
}

func NewEmbedder(cfg config.EmbedConfig) *Embedder {
	oaCfg := openai.DefaultConfig(cfg.APIKey)
	oaCfg.BaseURL = cfg.BaseURL
	return &Embedder{client: openai.NewClientWithConfig(oaCfg), model: cfg.Model}
}

// EmbedQueries and EmbedDocuments are identical calls: unlike ruri-v3, which
// was prefix-conditioned, OpenAI embeddings place queries and passages in one
// space with no side-specific prefix. Both methods stay because they are the
// seams persona.Store and Searcher depend on, and because keeping the call
// sites honest about which side they are embedding costs nothing.
func (e *Embedder) EmbedQueries(ctx context.Context, texts []string) ([][]float32, error) {
	return e.embed(ctx, texts)
}

func (e *Embedder) EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error) {
	return e.embed(ctx, texts)
}

func (e *Embedder) embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	out := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += maxBatch {
		end := min(start+maxBatch, len(texts))
		batch, err := e.embedBatch(ctx, texts[start:end])
		if err != nil {
			return nil, err
		}
		out = append(out, batch...)
	}
	return out, nil
}

func (e *Embedder) embedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	// OpenAI rejects an empty string with a 400 rather than returning a zero
	// vector, and a blank chunk carries no signal anyway, so substitute a
	// single space to keep the response indexes aligned with the input slice.
	inputs := make([]string, len(texts))
	for i, t := range texts {
		if strings.TrimSpace(t) == "" {
			inputs[i] = " "
			continue
		}
		inputs[i] = t
	}

	resp, err := e.client.CreateEmbeddings(ctx, openai.EmbeddingRequest{
		Input:      inputs,
		Model:      openai.EmbeddingModel(e.model),
		Dimensions: Dimensions,
	})
	if err != nil {
		return nil, fmt.Errorf("embed: create embeddings: %w", err)
	}
	if len(resp.Data) != len(inputs) {
		return nil, fmt.Errorf("embed: got %d embeddings for %d inputs", len(resp.Data), len(inputs))
	}

	// Place by Index rather than trusting response order: index is what the
	// API documents as authoritative, and a silent misalignment here would
	// attach every chunk's vector to the wrong text.
	vectors := make([][]float32, len(inputs))
	for _, d := range resp.Data {
		if d.Index < 0 || d.Index >= len(inputs) {
			return nil, fmt.Errorf("embed: response index %d out of range for %d inputs", d.Index, len(inputs))
		}
		if len(d.Embedding) != Dimensions {
			return nil, fmt.Errorf("embed: vector %d has %d dimensions, want %d",
				d.Index, len(d.Embedding), Dimensions)
		}
		vectors[d.Index] = d.Embedding
	}
	for i, v := range vectors {
		if v == nil {
			return nil, fmt.Errorf("embed: response missing index %d", i)
		}
	}
	return vectors, nil
}
