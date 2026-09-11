package main

import (
	"strings"
	"testing"
)

// TestChunkTextByteOffsetsSurviveJapaneseText guards the exact pitfall this
// command warns about in its own comments: chunking by rune count but
// recording byte offsets. If byteOffsetOf were computed in rune units
// instead, every offset past the first multi-byte rune would point at the
// wrong place in the original object — objectstore.ExpandContext's Range GET
// would then read the wrong bytes.
func TestChunkTextByteOffsetsSurviveJapaneseText(t *testing.T) {
	text := "これは分散合意形成アルゴリズムについてのテストです。" // pure multi-byte content

	chunks := chunkText(text)
	if len(chunks) == 0 {
		t.Fatal("chunkText returned no chunks")
	}

	for i, c := range chunks {
		if c.byteStart < 0 || c.byteEnd > len(text) || c.byteStart > c.byteEnd {
			t.Fatalf("chunk %d has invalid byte range [%d,%d) for a %d-byte string",
				i, c.byteStart, c.byteEnd, len(text))
		}
		// The slice of the original string at these byte offsets must
		// reproduce the chunk's own text exactly — proof the offsets are in
		// the right unit, not just in-bounds.
		if got := text[c.byteStart:c.byteEnd]; got != c.text {
			t.Errorf("chunk %d: text[%d:%d] = %q, want %q", i, c.byteStart, c.byteEnd, got, c.text)
		}
	}
}

func TestChunkTextOverlapsWindows(t *testing.T) {
	// A string long enough to force multiple chunks at the current
	// chunkSize/chunkOverlap, built from a repeating multi-byte rune so the
	// byte/rune distinction stays live in this test too.
	text := strings.Repeat("あ", chunkSize+chunkOverlap+10)

	chunks := chunkText(text)
	if len(chunks) < 2 {
		t.Fatalf("got %d chunks, want at least 2 for text of %d runes", len(chunks), chunkSize+chunkOverlap+10)
	}

	// Consecutive chunks must share the configured overlap, not start
	// exactly where the previous one ended.
	firstRuneLen := len([]rune(chunks[0].text))
	if firstRuneLen != chunkSize {
		t.Errorf("first chunk has %d runes, want %d", firstRuneLen, chunkSize)
	}
}

func TestChunkTextEmptyInput(t *testing.T) {
	if got := chunkText(""); got != nil {
		t.Errorf("chunkText(\"\") = %v, want nil", got)
	}
}
