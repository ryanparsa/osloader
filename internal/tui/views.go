package tui

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/lipgloss"

	"github.com/ryanparsa/osloader/internal/download"
	"github.com/ryanparsa/osloader/internal/format"
	"github.com/ryanparsa/osloader/internal/provider"
	"github.com/ryanparsa/osloader/internal/verify"
)

var (
	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15")).Background(lipgloss.Color("62")).Padding(0, 1)
	labelStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	valueStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	okStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	warnStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	failStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	bodyStyle   = lipgloss.NewStyle().Padding(1, 2)
	soonBadge   = lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render(" (coming soon)")
	errorHeader = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("203"))
)

// osItem is one operating system in the first picker.
type osItem struct {
	key       string
	name      string
	available bool
}

func (i osItem) Title() string {
	if i.available {
		return i.name
	}
	return i.name + soonBadge
}
func (i osItem) Description() string {
	if i.available {
		return "official installers, verified after download"
	}
	return "not implemented yet"
}
func (i osItem) FilterValue() string { return i.name }

// channelItem is one release train.
type channelItem struct {
	channel provider.Channel
	osName  string
}

func (i channelItem) Title() string       { return i.channel.Label }
func (i channelItem) Description() string { return i.osName + " · " + i.channel.ID }
func (i channelItem) FilterValue() string { return i.channel.Label }

// facetItem is one answer to a provider's question, such as an architecture.
type facetItem struct {
	facet provider.Facet
	value provider.FacetValue
	all   bool
}

func (i facetItem) Title() string {
	if i.all {
		return "Any " + strings.ToLower(i.facet.Label)
	}
	return i.value.Label
}

func (i facetItem) Description() string {
	if i.all {
		return "list everything"
	}
	return i.facet.Key + "=" + i.value.ID
}

func (i facetItem) FilterValue() string { return i.Title() }

// releaseItem is one installer.
type releaseItem struct {
	release provider.Release
}

func (i releaseItem) Title() string {
	version := i.release.Version
	if version == "" {
		version = "unknown version"
	}
	title := i.release.Title
	if title == "" {
		title = "macOS installer"
	}
	return fmt.Sprintf("%s  %s", version, title)
}

func (i releaseItem) Description() string {
	posted := "—"
	if !i.release.Posted.IsZero() {
		posted = i.release.Posted.Local().Format("2006-01-02")
	}
	return fmt.Sprintf("build %s · posted %s · %s · %s",
		orDash(i.release.Build), posted, humanBytes(i.release.Size), i.release.ID)
}

func (i releaseItem) FilterValue() string {
	return i.release.Version + " " + i.release.Build + " " + i.release.Title
}

func newList(title string, items []list.Item, width, height int, extra ...key.Binding) list.Model {
	delegate := list.NewDefaultDelegate()
	l := list.New(items, delegate, width, height)
	l.Title = title
	l.Styles.Title = titleStyle
	l.SetFilteringEnabled(true)

	// The status bar carries the item count and the active search, which is
	// what makes a filtered list explain itself — with 90-odd mirrors to pick
	// from, searching is the point rather than an extra.
	l.SetShowStatusBar(true)
	l.FilterInput.Prompt = "search: "
	l.KeyMap.Filter.SetHelp("/", "search")
	l.KeyMap.ClearFilter.SetHelp("esc", "clear search")

	keys := func() []key.Binding {
		return append([]key.Binding{
			key.NewBinding(key.WithKeys("b", "esc"), key.WithHelp("b", "back")),
			key.NewBinding(key.WithKeys("f"), key.WithHelp("f", "forward")),
		}, extra...)
	}
	l.AdditionalShortHelpKeys = keys
	l.AdditionalFullHelpKeys = keys
	return l
}

// viewDownload renders the transfer: one bar, the numbers that matter, and a
// grid of per-connection totals so a stalled worker is visible at a glance.
//
// Every line is assembled separately and joined at the end: a newline inside a
// styled span would put the ANSI reset on the next line and skew the layout.
func (m model) viewDownload() string {
	snap := m.snapshot

	lines := []string{
		titleStyle.Render(" downloading "),
		"",
		labelStyle.Render("file  ") + valueStyle.Render(m.destName),
		labelStyle.Render("from  ") + valueStyle.Render(m.artifact.URL),
		"",
		m.progress.ViewAs(snap.Fraction()),
		"",
		fmt.Sprintf("%s of %s   %s   ETA %s   elapsed %s",
			valueStyle.Render(humanBytes(snap.Done)),
			valueStyle.Render(humanBytes(snap.Total)),
			valueStyle.Render(humanRate(snap.Speed)),
			valueStyle.Render(humanDuration(snap.ETA)),
			valueStyle.Render(humanDuration(snap.Elapsed))),
	}

	if len(snap.Workers) > 1 {
		lines = append(lines, "", labelStyle.Render("connections"))
		lines = append(lines, connectionLines(snap.Workers)...)
	}

	if m.verbose {
		if events := m.logRing.Lines(); len(events) > 0 {
			lines = append(lines, "", labelStyle.Render("log"))
			for _, event := range events {
				lines = append(lines, dimStyle.Render("  "+event))
			}
		}
	}

	footer := "q cancels — progress is saved, rerun to resume"
	if m.stopping {
		footer = "stopping — saving progress…"
	}
	lines = append(lines, "", dimStyle.Render(footer))
	if cmd := m.downloadCommand(); cmd != "" {
		lines = append(lines, dimStyle.Render("next time: ")+valueStyle.Render(cmd))
	}
	return bodyStyle.Render(strings.Join(lines, "\n"))
}

// connectionLines renders one line per connection: which chunk it holds, how
// far into that chunk it is, and how fast it is moving. A connection that has
// gone quiet is obvious because its speed collapses while the others move on.
func connectionLines(workers []download.WorkerSnapshot) []string {
	showHost := distinctMirrors(workers) > 1
	hostWidth := 0
	if showHost {
		for _, w := range workers {
			if n := lipgloss.Width(w.Mirror); n > hostWidth {
				hostWidth = n
			}
		}
	}

	lines := make([]string, 0, len(workers))
	for i, w := range workers {
		label := "idle"
		if w.Active() {
			label = fmt.Sprintf("chunk %d", w.Chunk+1)
		}

		bar, percent := strings.Repeat(" ", barWidth), "    "
		if w.ChunkTotal > 0 {
			bar = miniBar(w.Fraction())
			percent = fmt.Sprintf("%3.0f%%", w.Fraction()*100)
		}

		line := "  " +
			padLeft(strconv.Itoa(i+1), 2) + "  " +
			padRight(label, 8) + " " +
			padLeft(humanBytes(w.Bytes), 10) + "  " +
			bar + " " + percent + "  " +
			padLeft(humanRate(w.Speed), 11)
		if showHost {
			line += "  " + padRight(w.Mirror, hostWidth)
		}
		lines = append(lines, dimStyle.Render(line))
	}
	return lines
}

// padLeft and padRight align a cell by how wide it *looks*, not how many bytes
// it takes: an em dash is three bytes but one column, and fmt's %10s would
// shift the whole row.
func padLeft(value string, width int) string {
	if gap := width - lipgloss.Width(value); gap > 0 {
		return strings.Repeat(" ", gap) + value
	}
	return value
}

func padRight(value string, width int) string {
	if gap := width - lipgloss.Width(value); gap > 0 {
		return value + strings.Repeat(" ", gap)
	}
	return value
}

// distinctMirrors counts how many different hosts the connections are using,
// so the host column only appears when a file actually has mirrors.
func distinctMirrors(workers []download.WorkerSnapshot) int {
	seen := make(map[string]struct{}, len(workers))
	for _, w := range workers {
		if w.Mirror != "" {
			seen[w.Mirror] = struct{}{}
		}
	}
	return len(seen)
}

// barWidth is the width of the per-connection progress bar.
const barWidth = 12

// miniBar draws a fixed-width bar for one connection's chunk.
func miniBar(fraction float64) string {
	filled := int(math.Round(fraction * barWidth))
	if filled < 0 {
		filled = 0
	}
	if filled > barWidth {
		filled = barWidth
	}
	return strings.Repeat("█", filled) + strings.Repeat("░", barWidth-filled)
}

// viewDone reports the result, check by check.
func (m model) viewDone() string {
	var lines []string

	if m.err != nil {
		lines = append(lines,
			errorHeader.Render("download failed"),
			"",
			failStyle.Render(m.err.Error()))
		if m.resumable {
			lines = append(lines, "", dimStyle.Render("partial data kept — run osloader again to resume"))
		}
		lines = append(lines, "", dimStyle.Render("q to quit"))
		return bodyStyle.Render(strings.Join(lines, "\n"))
	}

	header := " verified "
	if m.outcome != nil && m.outcome.AlreadyPresent {
		header = " already downloaded "
	}
	lines = append(lines,
		titleStyle.Render(header),
		"",
		labelStyle.Render("saved to  ")+valueStyle.Render(m.outcome.Path),
		"")
	lines = append(lines, reportLines(m.outcome.Report, m.artifact.Digest)...)
	if cmd := m.downloadCommand(); cmd != "" {
		lines = append(lines, "", dimStyle.Render("next time: ")+valueStyle.Render(cmd))
	}
	lines = append(lines, "", dimStyle.Render("q to quit"))
	return bodyStyle.Render(strings.Join(lines, "\n"))
}

// reportLines renders one line per check, with the marker styled but never the
// line break.
func reportLines(report *verify.Report, digest string) []string {
	if report == nil {
		return []string{warnStyle.Render("verification was skipped")}
	}

	lines := make([]string, 0, len(report.Checks)+1)
	for _, c := range report.Checks {
		var marker string
		switch c.Status {
		case verify.Pass:
			marker = okStyle.Render("  ✓ ")
		case verify.Fail:
			marker = failStyle.Render("  ✗ ")
		case verify.Warn:
			marker = warnStyle.Render("  ! ")
		default:
			marker = "    "
		}
		lines = append(lines, marker+fmt.Sprintf("%-10s %s", c.Name, c.Detail))
	}
	if digest != "" {
		lines = append(lines, dimStyle.Render(fmt.Sprintf("    %-10s %s (vendor value, not a file hash)", "digest", digest)))
	}
	return lines
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// Thin aliases so the view code reads cleanly.
func humanBytes(n int64) string            { return format.HumanBytes(n) }
func humanRate(bps float64) string         { return format.HumanRate(bps) }
func humanDuration(d time.Duration) string { return format.HumanDuration(d) }
