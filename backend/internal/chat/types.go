package chat

import "github.com/google/uuid"

// Message is one flattened, role-tagged statement used for RewriteQueries's
// pronoun resolution (see prompt.go's buildRewritePrompt) — a plain
// user/assistant history, distinct from the richer, speaker-attributed
// meeting.Turn the caller builds Engine.Reply's transcript from.
type Message struct {
	Role    string // "user" | "assistant"
	Content string
}

// Source is a citation as seen by the client: the persona-specific scores
// alongside the human-readable location, so the UI can show how much the
// persona moved each result rather than just the blended rank.
type Source struct {
	ChunkID    uuid.UUID `json:"chunkId"`
	DocumentID uuid.UUID `json:"documentId"`
	Title      string    `json:"title"`
	// ObjectKey is the chunk's MinIO object key, carried alongside the
	// already-presigned URL so a caller that persists this Source (see
	// personaexec.toCitationWires -> meeting.Citation.ObjectKey) can still
	// identify the underlying object after URL has expired. Without this,
	// every persisted citation's object_key column was silently empty — see
	// the code review finding this fixes.
	ObjectKey string  `json:"objectKey"`
	URL       string  `json:"url"`
	Relevance float32 `json:"relevance"`
	Affinity  float32 `json:"affinity"`
}
