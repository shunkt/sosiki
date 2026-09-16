package config

import (
	"fmt"
	"net/url"
)

// Logical database names within the single Postgres instance. Every service
// connects to exactly one of these — see the plan's "データベースの持ち主と
// 中身" table for who writes/reads which.
const (
	DBMeeting   = "kaigi_meeting"
	DBRegistry  = "kaigi_registry"
	DBPersona   = "kaigi_persona"
	DBKnowledge = "kaigi_knowledge"
)

// DatabaseConfig holds one fully-formed DSN per logical database, all
// pointing at the same Postgres instance (see docker-compose.yml's initdb
// scripts / k8s/base/postgres.yaml, which create all four databases on
// first boot).
type DatabaseConfig struct {
	Meeting   string
	Registry  string
	Persona   string
	Knowledge string
}

// DatabaseURLFor rewrites baseURL's path to dbName, keeping every other
// component (user, host, port, query string) intact. Building the DSN by
// string concatenation instead would require hand-rolling where the "?"
// query separator goes, which is exactly the kind of thing that silently
// breaks the moment sslmode or any other query param is added.
func DatabaseURLFor(baseURL, dbName string) (string, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("config: parse database base url: %w", err)
	}
	u.Path = "/" + dbName
	return u.String(), nil
}

// loadDatabaseConfig builds the four DSNs from POSTGRES_BASE_URL, unless an
// explicit per-service override (e.g. MEETING_DATABASE_URL) is set — the
// override always wins, so a single service can be pointed at a different
// Postgres instance without touching the others.
func loadDatabaseConfig() (DatabaseConfig, error) {
	base := env("POSTGRES_BASE_URL", "postgres://postgres:kaigi@localhost:5432?sslmode=disable")

	cfg := DatabaseConfig{}
	var err error
	if cfg.Meeting, err = dbURLFor(base, "MEETING_DATABASE_URL", DBMeeting); err != nil {
		return DatabaseConfig{}, err
	}
	if cfg.Registry, err = dbURLFor(base, "REGISTRY_DATABASE_URL", DBRegistry); err != nil {
		return DatabaseConfig{}, err
	}
	if cfg.Persona, err = dbURLFor(base, "PERSONA_DATABASE_URL", DBPersona); err != nil {
		return DatabaseConfig{}, err
	}
	if cfg.Knowledge, err = dbURLFor(base, "KNOWLEDGE_DATABASE_URL", DBKnowledge); err != nil {
		return DatabaseConfig{}, err
	}
	return cfg, nil
}

func dbURLFor(base, overrideKey, dbName string) (string, error) {
	if v := env(overrideKey, ""); v != "" {
		return v, nil
	}
	return DatabaseURLFor(base, dbName)
}
