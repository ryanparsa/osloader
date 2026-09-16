package logging

import (
	"bytes"
	"strings"
	"testing"
)

func TestCompactFormat(t *testing.T) {
	var buf bytes.Buffer
	New(&buf, true).Info("chunk retrying", "chunk", 3, "err", "connection reset")

	line := strings.TrimSpace(buf.String())
	for _, want := range []string{"chunk retrying", "chunk=3", "err=connection reset"} {
		if !strings.Contains(line, want) {
			t.Errorf("line %q missing %q", line, want)
		}
	}
	if !strings.HasPrefix(line, "0") && !strings.HasPrefix(line, "1") && !strings.HasPrefix(line, "2") {
		t.Errorf("line should start with a timestamp: %q", line)
	}
}

// TestQuietByDefault: a non-verbose logger must cost nothing and print nothing,
// so call sites can log unconditionally.
func TestQuietByDefault(t *testing.T) {
	var buf bytes.Buffer
	New(&buf, false).Info("should not appear")
	Discard().Info("should not appear either")

	if buf.Len() != 0 {
		t.Errorf("non-verbose logger wrote %q", buf.String())
	}
}

// TestRingKeepsTheLatestLines: the TUI panel shows a window onto the log, so
// the buffer must drop the oldest lines rather than grow forever.
func TestRingKeepsTheLatestLines(t *testing.T) {
	ring := NewRing(3)
	log := New(ring, true)
	for i := 1; i <= 5; i++ {
		log.Info("event", "n", i)
	}

	lines := ring.Lines()
	if len(lines) != 3 {
		t.Fatalf("ring holds %d lines, want 3", len(lines))
	}
	if !strings.Contains(lines[0], "n=3") || !strings.Contains(lines[2], "n=5") {
		t.Errorf("ring kept the wrong window: %v", lines)
	}
}
