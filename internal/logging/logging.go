// Package logging provides the verbose event log: compact one-line records
// that can go to stderr for the CLI or into a ring buffer for the TUI to show.
package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// New returns a logger writing compact lines to w. When verbose is false the
// logger discards everything, so callers can log unconditionally.
func New(w io.Writer, verbose bool) *slog.Logger {
	if !verbose || w == nil {
		return slog.New(discardHandler{})
	}
	return slog.New(&handler{w: w})
}

// Discard is a logger that drops every record.
func Discard() *slog.Logger { return slog.New(discardHandler{}) }

type discardHandler struct{}

func (discardHandler) Enabled(context.Context, slog.Level) bool  { return false }
func (discardHandler) Handle(context.Context, slog.Record) error { return nil }
func (h discardHandler) WithAttrs([]slog.Attr) slog.Handler      { return h }
func (h discardHandler) WithGroup(string) slog.Handler           { return h }

// handler formats records as "15:04:05.000  message  key=value", which reads
// better in a download log than slog's default key=value soup.
type handler struct {
	w    io.Writer
	mu   sync.Mutex
	base []slog.Attr
}

func (h *handler) Enabled(context.Context, slog.Level) bool { return true }

func (h *handler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString(r.Time.Format("15:04:05.000"))
	b.WriteString("  ")
	b.WriteString(r.Message)

	for _, a := range h.base {
		writeAttr(&b, a)
	}
	r.Attrs(func(a slog.Attr) bool {
		writeAttr(&b, a)
		return true
	})
	b.WriteByte('\n')

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := io.WriteString(h.w, b.String())
	return err
}

func writeAttr(b *strings.Builder, a slog.Attr) {
	b.WriteString("  ")
	b.WriteString(a.Key)
	b.WriteByte('=')
	switch v := a.Value.Any().(type) {
	case time.Duration:
		fmt.Fprintf(b, "%s", v.Round(time.Millisecond))
	case string:
		fmt.Fprintf(b, "%s", v)
	default:
		fmt.Fprintf(b, "%v", a.Value)
	}
}

func (h *handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := &handler{w: h.w, base: append(append([]slog.Attr(nil), h.base...), attrs...)}
	return next
}

func (h *handler) WithGroup(string) slog.Handler { return h }

// Ring keeps the most recent log lines in memory for the TUI to render. It is
// an io.Writer, so it plugs into New like any other destination.
type Ring struct {
	mu    sync.Mutex
	lines []string
	max   int
}

// NewRing returns a buffer holding at most max lines.
func NewRing(max int) *Ring {
	if max < 1 {
		max = 1
	}
	return &Ring{max: max}
}

func (r *Ring) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		if line == "" {
			continue
		}
		r.lines = append(r.lines, line)
	}
	if len(r.lines) > r.max {
		r.lines = append([]string(nil), r.lines[len(r.lines)-r.max:]...)
	}
	return len(p), nil
}

// Lines returns a copy of the buffered lines, oldest first.
func (r *Ring) Lines() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.lines...)
}
