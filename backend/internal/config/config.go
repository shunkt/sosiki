package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds runtime settings, all sourced from the environment so the
// binary can be configured without a rebuild.
type Config struct {
	// Addr is the TCP address the HTTP server listens on.
	Addr string
	// AllowedOrigins is the CORS allowlist for browser clients.
	AllowedOrigins []string

	// Databases holds the four logical-database DSNs within the single
	// Postgres instance. Each service opens exactly one — see dsn.go.
	Databases DatabaseConfig

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

	// A2A configures a persona pod's own A2A server identity.
	A2A A2AConfig

	// Registry configures how a persona pod finds and talks to the discovery
	// pod, and how the discovery pod itself is addressed by moderator.
	Registry RegistryConfig

	// Meeting tunes moderator's round-robin loop.
	Meeting MeetingConfig

	// PersonaSlug is which persona this cmd/persona process serves — it must
	// match a row in kaigi_persona (see cmd/seed) and the pod's own manifest.
	PersonaSlug string
}

// A2AConfig is a persona pod's own A2A server identity.
type A2AConfig struct {
	// PublicURL is this pod's own address as seen by OTHER pods (discovery
	// resolving its card, moderator dialing it) — never "localhost". In
	// compose this is the service name, in k8s the Service FQDN.
	PublicURL string
}

// RegistryConfig points at the discovery pod (from a persona pod's side,
// where to register; from moderator's side, where to list agents) and tunes
// the heartbeat that keeps a registration alive.
type RegistryConfig struct {
	URL string
	// HeartbeatInterval is how often a persona pod re-registers itself.
	HeartbeatInterval time.Duration
	// TTL is how long a registration is considered present after its last
	// heartbeat. Kept at 3x HeartbeatInterval by convention (see Validate) so
	// a single missed heartbeat does not flip a persona to absent.
	TTL time.Duration
}

// MeetingConfig bounds how large a single meeting turn can grow.
type MeetingConfig struct {
	// MaxRounds caps how many rounds a single POST .../turns can request,
	// regardless of what the client asks for — see meeting.Moderator.Run.
	MaxRounds int
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

// Load reads Config from the environment. It only fails on a malformed
// POSTGRES_BASE_URL (or an explicit per-database override) — everything
// else falls back to a default, per the env/envInt/envFloat/envBool helpers
// below.
func Load() (Config, error) {
	databases, err := loadDatabaseConfig()
	if err != nil {
		return Config{}, err
	}

	return Config{
		Addr:           env("ADDR", ":8080"),
		AllowedOrigins: splitAndTrim(env("ALLOWED_ORIGINS", "http://localhost:5173")),
		Databases:      databases,
		PersonaSlug:    env("PERSONA_SLUG", ""),
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
		A2A: A2AConfig{
			PublicURL: env("A2A_PUBLIC_URL", ""),
		},
		Registry: RegistryConfig{
			URL:               env("REGISTRY_URL", "http://localhost:8081"),
			HeartbeatInterval: envDuration("REGISTRY_HEARTBEAT_INTERVAL", 15*time.Second),
			TTL:               envDuration("REGISTRY_TTL", 45*time.Second),
		},
		Meeting: MeetingConfig{
			MaxRounds: envInt("MEETING_MAX_ROUNDS", 3),
		},
	}, nil
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

// ValidateRegistry checks the heartbeat/TTL relationship a persona pod's
// registry.Client depends on: TTL must give a heartbeat at least one missed
// beat of slack, or a single delayed heartbeat flips the persona to absent.
func (c Config) ValidateRegistry() error {
	if c.Registry.TTL <= c.Registry.HeartbeatInterval*2 {
		return fmt.Errorf("REGISTRY_TTL (%s) must be more than 2x REGISTRY_HEARTBEAT_INTERVAL (%s)",
			c.Registry.TTL, c.Registry.HeartbeatInterval)
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

func envDuration(key string, fallback time.Duration) time.Duration {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return fallback
	}
	return d
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
