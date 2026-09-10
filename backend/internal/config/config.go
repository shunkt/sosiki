package config

import (
	"os"
	"strings"
)

// Config holds runtime settings, all sourced from the environment so the
// binary can be configured without a rebuild.
type Config struct {
	// Addr is the TCP address the HTTP server listens on.
	Addr string
	// AllowedOrigins is the CORS allowlist for browser clients.
	AllowedOrigins []string
}

func Load() Config {
	return Config{
		Addr:           env("ADDR", ":8080"),
		AllowedOrigins: splitAndTrim(env("ALLOWED_ORIGINS", "http://localhost:5173")),
	}
}

func env(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
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
