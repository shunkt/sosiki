// Package persona defines what a persona is: knowledge access shaped by a
// personality that biases retrieval and generation.
package persona

import "github.com/google/uuid"

// Interest biases retrieval toward (positive weight) or away from (negative
// weight) a topic. Embedding is populated by the store using the same
// "検索クエリ: " prefix as retrieval queries — see retrieval.EmbedQueries.
type Interest struct {
	Topic     string
	Weight    float32 // -1..1
	Embedding []float32
}

// Verbosity controls how much the persona says, expressed in the system
// prompt rather than as a token cap so the model can still finish a thought.
type Verbosity string

const (
	VerbosityConcise  Verbosity = "concise"
	VerbosityBalanced Verbosity = "balanced"
	VerbosityDetailed Verbosity = "detailed"
)

// Personality is the "direction in which information is processed" — the
// part of a persona that is not knowledge. It acts at three points: query
// rewriting, rerank blending, and the system prompt.
type Personality struct {
	Stance     string
	Interests  []Interest
	Skepticism float32 // 0..1; raises the rerank relevance threshold
	Verbosity  Verbosity
}

type Persona struct {
	ID          uuid.UUID
	Name        string
	Personality Personality
}
