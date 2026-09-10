package chat

import (
	"context"
	"errors"
	"fmt"
	"io"

	openai "github.com/sashabaranov/go-openai"

	"github.com/shun/kaigi/backend/internal/config"
)

// DeepSeekClient adapts go-openai to LLMClient. DeepSeek's API is OpenAI-
// compatible but not identical (see the GOTCHAs in prompt.go and engine.go);
// every difference is meant to live in this file, not in the engine.
type DeepSeekClient struct {
	client *openai.Client
}

func NewDeepSeekClient(cfg config.LLMConfig) *DeepSeekClient {
	oaCfg := openai.DefaultConfig(cfg.APIKey)
	oaCfg.BaseURL = cfg.BaseURL
	return &DeepSeekClient{client: openai.NewClientWithConfig(oaCfg)}
}

func (c *DeepSeekClient) CreateChatCompletion(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	resp, err := c.client.CreateChatCompletion(ctx, toOpenAIRequest(req))
	if err != nil {
		return ChatResponse{}, fmt.Errorf("deepseek: create chat completion: %w", err)
	}
	if len(resp.Choices) == 0 {
		return ChatResponse{}, fmt.Errorf("deepseek: response had no choices")
	}
	return ChatResponse{Content: resp.Choices[0].Message.Content}, nil
}

func (c *DeepSeekClient) CreateChatCompletionStream(ctx context.Context, req ChatRequest) (ChatStream, error) {
	req.Stream = true
	stream, err := c.client.CreateChatCompletionStream(ctx, toOpenAIRequest(req))
	if err != nil {
		return nil, fmt.Errorf("deepseek: create stream: %w", err)
	}
	return &deepSeekStream{stream: stream}, nil
}

func toOpenAIRequest(req ChatRequest) openai.ChatCompletionRequest {
	messages := make([]openai.ChatCompletionMessage, len(req.Messages))
	for i, m := range req.Messages {
		messages[i] = openai.ChatCompletionMessage{Role: m.Role, Content: m.Content}
	}
	out := openai.ChatCompletionRequest{
		Model:    req.Model,
		Messages: messages,
		Stream:   req.Stream,
	}
	if req.JSONMode {
		out.ResponseFormat = &openai.ChatCompletionResponseFormat{
			Type: openai.ChatCompletionResponseFormatTypeJSONObject,
		}
	}
	return out
}

type deepSeekStream struct {
	stream *openai.ChatCompletionStream
}

func (s *deepSeekStream) Recv() (ChatStreamChunk, error) {
	resp, err := s.stream.Recv()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return ChatStreamChunk{}, io.EOF
		}
		return ChatStreamChunk{}, fmt.Errorf("deepseek: stream recv: %w", err)
	}
	choices := make([]ChatStreamChoice, len(resp.Choices))
	for i, c := range resp.Choices {
		choices[i] = ChatStreamChoice{
			Content:      c.Delta.Content,
			FinishReason: string(c.FinishReason),
		}
	}
	return ChatStreamChunk{Choices: choices}, nil
}

func (s *deepSeekStream) Close() error {
	return s.stream.Close()
}
