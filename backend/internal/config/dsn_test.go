package config

import "testing"

func TestDatabaseURLFor(t *testing.T) {
	got, err := DatabaseURLFor("postgres://u:p@h:5432?sslmode=disable", "kaigi_meeting")
	if err != nil {
		t.Fatalf("DatabaseURLFor: %v", err)
	}
	want := "postgres://u:p@h:5432/kaigi_meeting?sslmode=disable"
	if got != want {
		t.Errorf("DatabaseURLFor() = %q, want %q", got, want)
	}
}

func TestDatabaseURLForReplacesExistingPath(t *testing.T) {
	got, err := DatabaseURLFor("postgres://u:p@h:5432/oldname?sslmode=disable", "kaigi_knowledge")
	if err != nil {
		t.Fatalf("DatabaseURLFor: %v", err)
	}
	want := "postgres://u:p@h:5432/kaigi_knowledge?sslmode=disable"
	if got != want {
		t.Errorf("DatabaseURLFor() = %q, want %q", got, want)
	}
}

func TestDatabaseURLForInvalid(t *testing.T) {
	if _, err := DatabaseURLFor("not a url with spaces and :://bad", "kaigi_meeting"); err == nil {
		t.Error("DatabaseURLFor() error = nil, want error for invalid base URL")
	}
}

func TestLoadDatabaseConfigOverridePriority(t *testing.T) {
	t.Setenv("POSTGRES_BASE_URL", "postgres://postgres:kaigi@postgres:5432?sslmode=disable")
	t.Setenv("MEETING_DATABASE_URL", "postgres://custom:pw@other-host:5432/custom_db?sslmode=require")

	cfg, err := loadDatabaseConfig()
	if err != nil {
		t.Fatalf("loadDatabaseConfig: %v", err)
	}
	if cfg.Meeting != "postgres://custom:pw@other-host:5432/custom_db?sslmode=require" {
		t.Errorf("Meeting DSN = %q, want the MEETING_DATABASE_URL override", cfg.Meeting)
	}
	if cfg.Registry != "postgres://postgres:kaigi@postgres:5432/kaigi_registry?sslmode=disable" {
		t.Errorf("Registry DSN = %q, want derived from POSTGRES_BASE_URL", cfg.Registry)
	}
}

func TestLoadDatabaseConfigDerivesAllFour(t *testing.T) {
	t.Setenv("POSTGRES_BASE_URL", "postgres://postgres:kaigi@postgres:5432?sslmode=disable")

	cfg, err := loadDatabaseConfig()
	if err != nil {
		t.Fatalf("loadDatabaseConfig: %v", err)
	}
	tests := []struct {
		name string
		got  string
		want string
	}{
		{"Meeting", cfg.Meeting, "postgres://postgres:kaigi@postgres:5432/kaigi_meeting?sslmode=disable"},
		{"Registry", cfg.Registry, "postgres://postgres:kaigi@postgres:5432/kaigi_registry?sslmode=disable"},
		{"Persona", cfg.Persona, "postgres://postgres:kaigi@postgres:5432/kaigi_persona?sslmode=disable"},
		{"Knowledge", cfg.Knowledge, "postgres://postgres:kaigi@postgres:5432/kaigi_knowledge?sslmode=disable"},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("%s = %q, want %q", tt.name, tt.got, tt.want)
		}
	}
}
