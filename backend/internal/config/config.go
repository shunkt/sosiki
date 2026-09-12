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

	// Embed points the OpenAI embeddings client at a model whose output width
	// must match vector(1536) in SQL — see retrieval.Dimensions.
	Embed EmbedConfig

	// LLM is the OpenAI chat client. Embed and LLM carry the same BaseURL and
	// APIKey by default; they stay separate structs so each client constructor
	// takes only what it needs.
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
	// Region is supplied rather than discovered: see objectstore.New for why a
	// lookup would dial the wrong host. MinIO's own default is us-east-1.
	Region string
}

// LLMConfig targets OpenAI's chat completions API. BaseURL stays
// configurable so an OpenAI-compatible gateway can be swapped in without
// a rebuild.
type LLMConfig struct {
	BaseURL string
	APIKey  string
	Model   string
}

// EmbedConfig targets the OpenAI embeddings API. Model must be a
// text-embedding-3 family model: the request always sends the dimensions
// parameter, which older models (ada-002) reject.
type EmbedConfig struct {
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
	// RelevanceFloor and SkepticismSpan define the minimum cosine similarity a
	// chunk needs before it can be cited: floor + span*Skepticism. They are
	// tunable because the right values depend on the embedding model's score
	// distribution over the corpus, which only measurement settles — the
	// defaults are calibrated for text-embedding-3-small over Japanese prose.
	RelevanceFloor float64
	SkepticismSpan float64
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
			Region:         env("MINIO_REGION", "us-east-1"),
		},
		Embed: EmbedConfig{
			BaseURL: env("OPENAI_BASE_URL", "https://api.openai.com/v1"),
			APIKey:  env("OPENAI_API_KEY", ""),
			Model:   env("EMBED_MODEL", "text-embedding-3-small"),
		},
		LLM: LLMConfig{
			BaseURL: env("OPENAI_BASE_URL", "https://api.openai.com/v1"),
			APIKey:  env("OPENAI_API_KEY", ""),
			Model:   env("CHAT_MODEL", "gpt-5-mini"),
		},
		Retrieval: RetrievalConfig{
			CandidateK:       envInt("CANDIDATE_K", 40),
			FinalN:           envInt("FINAL_N", 8),
			PersonaInfluence: envFloat("PERSONA_INFLUENCE", 0.25),
			ContextPadBytes:  envInt("CONTEXT_PAD_BYTES", 1200),
			RelevanceFloor:   envFloat("RELEVANCE_FLOOR", 0.25),
			SkepticismSpan:   envFloat("RELEVANCE_SKEPTICISM_SPAN", 0.20),
		},
	}
}

// Validate rejects configurations that would fail later at request time. The
// conversation API is meaningless without an LLM key, so that is a boot error
// rather than a 500 on the first message.
func (c Config) Validate() error {
	if c.LLM.APIKey == "" {
		return fmt.Errorf("OPENAI_API_KEY is required")
	}
	if c.Retrieval.PersonaInfluence < 0 || c.Retrieval.PersonaInfluence > 1 {
		return fmt.Errorf("PERSONA_INFLUENCE must be within 0..1, got %v",
			c.Retrieval.PersonaInfluence)
	}
	if c.Retrieval.FinalN > c.Retrieval.CandidateK {
		return fmt.Errorf("FINAL_N (%d) cannot exceed CANDIDATE_K (%d)",
			c.Retrieval.FinalN, c.Retrieval.CandidateK)
	}
	if c.Retrieval.RelevanceFloor < 0 || c.Retrieval.RelevanceFloor > 1 {
		return fmt.Errorf("RELEVANCE_FLOOR must be within 0..1, got %v",
			c.Retrieval.RelevanceFloor)
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
