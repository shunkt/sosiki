package chat

import "context"

// ChatMessage is a provider-neutral message. Kept separate from go-openai's
// type so a fake LLMClient in tests needs no import of go-openai at all.
type ChatMessage struct {
	Role    string // "system" | "user" | "assistant"
	Content string
}

type ChatRequest struct {
	Model    string
	Stream   bool
	Messages []ChatMessage
	// JSONMode asks the provider to constrain output to a JSON object. The
	// caller is still responsible for describing the desired shape in the
	// prompt — json_schema enforcement is not assumed to be available.
	JSONMode bool
}

type ChatResponse struct {
	Content string
}

// ChatStreamChoice mirrors an OpenAI-compatible stream chunk's single choice.
// Content is the incremental delta, not the accumulated text.
type ChatStreamChoice struct {
	Content      string
	FinishReason string
}

// ChatStreamChunk models one SSE frame from the provider. Choices can be
// empty — OpenAI-compatible streams interleave usage-only chunks that carry
// no delta — so callers must check len(Choices) before indexing.
type ChatStreamChunk struct {
	Choices []ChatStreamChoice
}

// ChatStream is the seam over a live streaming response. Recv returns
// io.EOF when the stream ends normally.
type ChatStream interface {
	Recv() (ChatStreamChunk, error)
	Close() error
}

// LLMClient is the seam over the OpenAI-compatible DeepSeek client, kept
// narrow so the engine's and prompt-rewriter's tests can drive a fake
// implementation without a live API key.
type LLMClient interface {
	CreateChatCompletion(ctx context.Context, req ChatRequest) (ChatResponse, error)
	CreateChatCompletionStream(ctx context.Context, req ChatRequest) (ChatStream, error)
}
