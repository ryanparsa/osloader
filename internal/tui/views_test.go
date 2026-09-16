package tui

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/ryanparsa/osloader/internal/download"
	"github.com/ryanparsa/osloader/internal/provider"
)

var ansi = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

func visible(s string) string { return ansi.ReplaceAllString(s, "") }

func downloadingModel(t *testing.T, workers []download.WorkerSnapshot) model {
	t.Helper()
	return downloadingWith(t, Config{OutDir: ".", Connections: download.DefaultConnections}, workers)
}

// downloadingWith builds a model parked on the download screen with a known
// config, so the command hint and layout are deterministic.
func downloadingWith(t *testing.T, cfg Config, workers []download.WorkerSnapshot) model {
	t.Helper()
	m := sizedWith(t, cfg)
	m.state = stateDownloading
	m.artifact = provider.Artifact{URL: "https://swcdn.apple.com/x/InstallAssistant.pkg"}
	m.destName = "./macOS_27.0_26A428_InstallAssistant.pkg"
	m.release = provider.Release{ID: "142-15488", Version: "27.0", Build: "26A428"}
	m.channel = "public"
	m.provKey = "macos"
	m.snapshot = download.Snapshot{
		Total: 18400314350, Done: 1503238553, Speed: 15 << 20,
		Workers: workers,
	}
	return m
}

func sampleWorkers() []download.WorkerSnapshot {
	return []download.WorkerSnapshot{
		{Bytes: 181633024, Speed: 2 << 20, Chunk: 0, ChunkDone: 181633024, ChunkTotal: 2300039293},
		{Bytes: 212047872, Speed: 2 << 20, Chunk: 1, ChunkDone: 212047872, ChunkTotal: 2300039293},
		{Bytes: 1258291200, Speed: 3 << 20, Chunk: 2, ChunkDone: 1258291200, ChunkTotal: 2300039293},
		{Bytes: 189599744, Speed: 0, Chunk: -1},
	}
}

// connectionRows pulls the per-connection lines out of a rendered view.
func connectionRows(t *testing.T, m model) []string {
	t.Helper()
	var rows []string
	inBlock := false
	for _, line := range strings.Split(visible(m.View()), "\n") {
		switch {
		case strings.Contains(line, "connections"):
			inBlock = true
		case inBlock && strings.TrimSpace(line) == "":
			inBlock = false
		case inBlock:
			rows = append(rows, line)
		}
	}
	return rows
}

// TestOneLinePerConnection: each connection gets its own row, with its chunk,
// bytes, a bar and its own speed.
func TestOneLinePerConnection(t *testing.T) {
	m := downloadingModel(t, sampleWorkers())
	rows := connectionRows(t, m)

	if len(rows) != len(sampleWorkers()) {
		t.Fatalf("rendered %d connection rows, want %d:\n%s", len(rows), len(sampleWorkers()), visible(m.View()))
	}
	for i, row := range rows {
		if !strings.Contains(row, fmt.Sprint(i+1)) {
			t.Errorf("row %d does not carry its number: %q", i, row)
		}
	}
	if !strings.Contains(rows[0], "chunk 1") {
		t.Errorf("row 0 does not name its chunk: %q", rows[0])
	}
	if !strings.Contains(rows[0], "173.2 MiB") || !strings.Contains(rows[0], "2.0 MiB/s") {
		t.Errorf("row 0 missing its own bytes and speed: %q", rows[0])
	}
	if !strings.Contains(rows[3], "idle") {
		t.Errorf("an idle connection should say so: %q", rows[3])
	}
	// Chunk 3 is 54%% done, so its bar must be partly filled and partly not.
	if !strings.Contains(rows[2], "█") || !strings.Contains(rows[2], "░") {
		t.Errorf("row 2 has no partial bar: %q", rows[2])
	}
}

// TestConnectionRowsAlign is the regression test for a layout that drifted: a
// newline inside a styled span put the ANSI reset on the following line, so
// every connection after the first was indented further than the last.
func TestConnectionRowsAlign(t *testing.T) {
	m := downloadingModel(t, sampleWorkers())
	rows := connectionRows(t, m)
	if len(rows) < 2 {
		t.Fatalf("not enough rows to compare:\n%s", visible(m.View()))
	}

	indent := func(s string) int { return len(s) - len(strings.TrimLeft(s, " ")) }
	barAt := func(s string) int { return strings.IndexAny(s, "█░") }

	for _, row := range rows[1:] {
		if indent(row) != indent(rows[0]) {
			t.Errorf("row %q indented %d, want %d", row, indent(row), indent(rows[0]))
		}
		// An idle connection draws no bar, but a row that has one must put it
		// in the same column as every other row.
		if at := barAt(row); at >= 0 && at != barAt(rows[0]) {
			t.Errorf("bar column moves: %q starts at %d, first row at %d", row, at, barAt(rows[0]))
		}
		if lipgloss.Width(row) != lipgloss.Width(rows[0]) {
			t.Errorf("row widths differ (%d vs %d):\n%q\n%q",
				lipgloss.Width(row), lipgloss.Width(rows[0]), row, rows[0])
		}
	}
}

// TestVerboseShowsEventLog: with --verbose the download screen carries the
// recent log lines, so a stall or retry is visible without leaving the TUI.
func TestVerboseShowsEventLog(t *testing.T) {
	m := downloadingWith(t, Config{OutDir: ".", Connections: download.DefaultConnections, Verbose: true}, sampleWorkers())
	m.logger.Info("chunk retrying", "chunk", 3, "err", "connection reset")

	view := visible(m.View())
	if !strings.Contains(view, "log") || !strings.Contains(view, "chunk retrying") {
		t.Errorf("verbose view does not show the event log:\n%s", view)
	}
	if !strings.Contains(view, "connection reset") {
		t.Errorf("event detail missing:\n%s", view)
	}

	quiet := downloadingModel(t, sampleWorkers())
	if strings.Contains(visible(quiet.View()), "\n  log") {
		t.Error("the log panel should only appear with --verbose")
	}
}

// TestDownloadCommandHint gives the user the exact command to skip the picker
// next time.
func TestDownloadCommandHint(t *testing.T) {
	m := downloadingModel(t, sampleWorkers())

	// Fully specified: someone else can paste this and get this exact file.
	want := "osloader download --os macos --channel public --product 142-15488 --version 27.0 --build 26A428"
	if got := m.downloadCommand(); got != want {
		t.Errorf("hint = %q, want %q", got, want)
	}
	if !strings.Contains(visible(m.View()), want) {
		t.Errorf("the download screen does not show the hint:\n%s", visible(m.View()))
	}

	// Local settings appear only when they differ from the defaults.
	m.channel = "beta"
	m.cfg.OutDir = "/tmp/os images"
	m.cfg.Connections = 12
	m.verbose = true
	want = "osloader download --os macos --channel beta --product 142-15488 --version 27.0 --build 26A428" +
		" --out '/tmp/os images' --connections 12 --verbose"
	if got := m.downloadCommand(); got != want {
		t.Errorf("hint = %q, want %q", got, want)
	}

	// No release chosen yet means no hint to give.
	empty := sized(t)
	if got := empty.downloadCommand(); got != "" {
		t.Errorf("hint without a release = %q, want empty", got)
	}
}
