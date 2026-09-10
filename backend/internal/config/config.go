package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config holds runtime settings, all sourced from the environment so the
// binary can be configured without a rebuild.
type Config struct {
	// Addr is the TCP address the HTTP server listens on.
	Addr string
	// AllowedOrigins is the CORS allowlist for browser clients.
	AllowedOrigins []string

	// DatabaseURL is the Postgres DSN. The database must have pgvector.
	DatabaseURL string

	// ObjectStore holds the S3-compatible (MinIO) connection.
	ObjectStore ObjectStoreConfig

	// EmbedURL and RerankURL are Text Embeddings Inference base URLs. TEI
	// serves one model per process, so these are separate hosts.
	EmbedURL  string
	RerankURL string

	// LLM points an OpenAI-compatible client at DeepSeek.
	LLM LLMConfig

	// Retrieval tunes the persona-weighted rerank.
	Retrieval RetrievalConfig
}

type ObjectStoreConfig struct {
	// Endpoint is what the backend dials.
	Endpoint string
	// PublicEndpoint is baked into presigned URLs. It differs from Endpoint
	// whenever the backend reaches MinIO by a name the browser cannot resolve.
	PublicEndpoint string
	AccessKey      string
	SecretKey      string
	Bucket         string
	UseSSL         bool
}

// LLMConfig targets DeepSeek through an OpenAI-compatible client. DeepSeek
// publishes no Go SDK of its own, so the OpenAI client with a swapped base URL
// is the supported path.
type LLMConfig struct {
	BaseURL string
	APIKey  string
	Model   string
}

type RetrievalConfig struct {
	// CandidateK is how many chunks pgvector returns per query.
	CandidateK int
	// FinalN is how many survive reranking and reach the prompt.
	FinalN int
	// PersonaInfluence is the lambda blending cross-encoder relevance with
	// persona affinity. 0 degenerates to plain RAG.
	PersonaInfluence float64
	// ContextPadBytes is how far either side of a chunk the Range GET reads.
	ContextPadBytes int
}

func Load() Config {
	return Config{
		Addr:           env("ADDR", ":8080"),
		AllowedOrigins: splitAndTrim(env("ALLOWED_ORIGINS", "http://localhost:5173")),
		DatabaseURL: env("DATABASE_URL",
			"postgres://postgres:kaigi@localhost:5432/kaigi?sslmode=disable"),
		ObjectStore: ObjectStoreConfig{
			Endpoint:       env("MINIO_ENDPOINT", "localhost:9000"),
			PublicEndpoint: env("MINIO_PUBLIC_ENDPOINT", "localhost:9000"),
			AccessKey:      env("MINIO_ACCESS_KEY", "minioadmin"),
			SecretKey:      env("MINIO_SECRET_KEY", "minioadmin"),
			Bucket:         env("MINIO_BUCKET", "kaigi-knowledge"),
			UseSSL:         envBool("MINIO_USE_SSL", false),
		},
		EmbedURL:  env("EMBED_URL", "http://localhost:8081"),
		RerankURL: env("RERANK_URL", "http://localhost:8082"),
		LLM: LLMConfig{
			BaseURL: env("DEEPSEEK_BASE_URL", "https://api.deepseek.com"),
			APIKey:  env("DEEPSEEK_API_KEY", ""),
			// deepseek-v4-flash is a retired alias; do not depend on its routing.
			Model: env("CHAT_MODEL", "deepseek-flash"),
		},
		Retrieval: RetrievalConfig{
			CandidateK:       envInt("CANDIDATE_K", 40),
			FinalN:           envInt("FINAL_N", 8),
			PersonaInfluence: envFloat("PERSONA_INFLUENCE", 0.25),
			ContextPadBytes:  envInt("CONTEXT_PAD_BYTES", 1200),
		},
	}
}

// Validate rejects configurations that would fail later at request time. The
// conversation API is meaningless without an LLM key, so that is a boot error
// rather than a 500 on the first message.
func (c Config) Validate() error {
	if c.LLM.APIKey == "" {
		return fmt.Errorf("DEEPSEEK_API_KEY is required")
	}
	if c.Retrieval.PersonaInfluence < 0 || c.Retrieval.PersonaInfluence > 1 {
		return fmt.Errorf("PERSONA_INFLUENCE must be within 0..1, got %v",
			c.Retrieval.PersonaInfluence)
	}
	if c.Retrieval.FinalN > c.Retrieval.CandidateK {
		return fmt.Errorf("FINAL_N (%d) cannot exceed CANDIDATE_K (%d)",
			c.Retrieval.FinalN, c.Retrieval.CandidateK)
	}
	return nil
}

func env(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

func envFloat(key string, fallback float64) float64 {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return fallback
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return fallback
	}
	return f
}

func envBool(key string, fallback bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return fallback
	}
	return b
}

func splitAndTrim(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
