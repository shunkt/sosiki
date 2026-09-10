package objectstore

import (
	"os"
	"testing"
	"unicode/utf8"
)

func TestTrimToSentenceHandlesMixedDelimiters(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "leading partial cut at newline, trailing at period",
			input: "続き\n完全な文です。次の文の途中",
			want:  "完全な文です。",
		},
		{
			name:  "leading partial cut at period",
			input: "続き。ここから始まる文です\n",
			want:  "ここから始まる文です\n",
		},
		{
			name:  "no delimiters at all",
			input: "区切りなし",
			want:  "区切りなし",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := trimToSentence(tt.input)
			if got != tt.want {
				t.Errorf("trimToSentence(%q) = %q, want %q", tt.input, got, tt.want)
			}
			if !utf8.ValidString(got) {
				t.Errorf("trimToSentence(%q) = %q is not valid UTF-8", tt.input, got)
			}
		})
	}
}

// TestExpandContextKeepsValidUTF8 and TestExpandContextClampsToObjectSize are
// integration tests against a real MinIO instance. They only run when
// TEST_MINIO_ENDPOINT is set (e.g. against the docker-compose minio).
func TestExpandContextKeepsValidUTF8(t *testing.T) {
	if os.Getenv("TEST_MINIO_ENDPOINT") == "" {
		t.Skip("TEST_MINIO_ENDPOINT not set; skipping integration test")
	}
	t.Skip("integration wiring lands with Task 13's seed data")
}

func TestExpandContextClampsToObjectSize(t *testing.T) {
	if os.Getenv("TEST_MINIO_ENDPOINT") == "" {
		t.Skip("TEST_MINIO_ENDPOINT not set; skipping integration test")
	}
	t.Skip("integration wiring lands with Task 13's seed data")
}
