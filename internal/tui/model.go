// Package tui is the interactive front end: pick an OS, pick a channel, pick a
// release, watch it download, see what was verified.
package tui

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/ryanparsa/osloader/internal/download"
	"github.com/ryanparsa/osloader/internal/logging"
	"github.com/ryanparsa/osloader/internal/provider"
	"github.com/ryanparsa/osloader/internal/ua"
	"github.com/ryanparsa/osloader/internal/verify"
)

// Config carries the command-line defaults into the interactive flow.
type Config struct {
	OutDir      string
	Connections int
	LimitRate   int64
	UserAgent   string
	Verbose     bool
	SkipVerify  bool
}

type state int

// verboseLogLines is how much of the event log the download screen shows.
const verboseLogLines = 8

const (
	stateOS state = iota
	stateChannel
	stateFacet
	stateReleases
	stateDownloading
	stateDone
)

type (
	releasesMsg struct {
		gen      int
		releases []provider.Release
		err      error
	}
	artifactMsg struct {
		gen      int
		artifact provider.Artifact
		err      error
	}
	doneMsg struct {
		outcome *download.Outcome
		err     error
	}
	tickMsg struct{}
)

// canceller is shared by pointer between every copy of the model: Bubble Tea
// passes the model around by value, and Init in particular runs on a copy, so
// storing the cancel function in a plain field would lose it - which is
// exactly what made Ctrl-C do nothing during a direct download.
type canceller struct {
	mu sync.Mutex
	fn context.CancelFunc
}

func (c *canceller) set(fn context.CancelFunc) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fn = fn
}

// cancel stops the download and reports whether there was one to stop.
func (c *canceller) cancel() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fn == nil {
		return false
	}
	c.fn()
	return true
}

// sampler hands the latest progress from the download goroutine to the UI
// without a channel per update: the UI reads whatever the most recent sample is.
type sampler struct {
	mu   sync.Mutex
	snap download.Snapshot
}

func (s *sampler) set(snap download.Snapshot) {
	s.mu.Lock()
	s.snap = snap
	s.mu.Unlock()
}

func (s *sampler) get() download.Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snap
}

type model struct {
	cfg           Config
	state         state
	width, height int

	list     list.Model
	spinner  spinner.Model
	progress progress.Model

	prov      provider.Provider
	channel   string
	releases  []provider.Release
	allFacets []provider.Facet
	facets    []provider.Facet
	facetAt   int
	selection provider.Selection
	sortMode  provider.SortMode
	sortDesc  bool
	release   provider.Release
	artifact  provider.Artifact
	destName  string

	loading bool
	status  string

	sampler  *sampler
	snapshot download.Snapshot
	stopper  *canceller
	ctx      context.Context

	outcome   *download.Outcome
	err       error
	resumable bool

	provKey string
	back    []screen
	forward []screen
	gen     int

	direct   bool
	stopping bool
	verbose  bool
	logRing  *logging.Ring
	logger   *slog.Logger
}

// Direct is a download chosen on the command line rather than in the picker.
// The same screens render it, so `osloader download` and the interactive flow
// look and behave identically.
type Direct struct {
	OSKey     string
	Channel   string
	Release   provider.Release
	Artifact  provider.Artifact
	Mirrors   []string
	Selection provider.Selection
}

// Run starts the interactive picker.
func Run(ctx context.Context, cfg Config) error {
	return run(ctx, newModel(ctx, cfg))
}

// RunDownload skips the picker and goes straight to the download screen for an
// already resolved release.
func RunDownload(ctx context.Context, cfg Config, d Direct) error {
	m := newModel(ctx, cfg)
	m.direct = true
	m.state = stateDownloading
	m.provKey = d.OSKey
	m.channel = d.Channel
	m.selection = d.Selection
	m.release = d.Release
	m.artifact = d.Artifact
	m.artifact.Mirrors = append(m.artifact.Mirrors, d.Mirrors...)
	m.destName = m.artifact.Target(cfg.OutDir).Dest
	return run(ctx, m)
}

func run(ctx context.Context, m model) error {
	program := tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(ctx))

	final, err := program.Run()
	// The alt screen is gone by now, so anything printed here survives in the
	// scrollback - which is exactly what a half-finished download needs.
	if last, ok := final.(model); ok {
		if hint := last.exitHint(); hint != "" {
			fmt.Println(hint)
		}
	}
	if err != nil {
		if errors.Is(err, tea.ErrProgramKilled) {
			return ctx.Err()
		}
		return err
	}
	return nil
}

func newModel(ctx context.Context, cfg Config) model {
	items := make([]list.Item, 0, len(provider.Keys()))
	for _, key := range provider.Keys() {
		p, err := provider.Get(key)
		if err != nil {
			continue
		}
		items = append(items, osItem{
			key:         key,
			name:        p.Name(),
			description: provider.Describe(p),
			available:   p.Available(),
		})
	}

	spin := spinner.New(spinner.WithSpinner(spinner.Dot))
	ring := logging.NewRing(verboseLogLines)
	return model{
		verbose:  cfg.Verbose,
		logRing:  ring,
		logger:   logging.New(ring, cfg.Verbose),
		cfg:      cfg,
		ctx:      ctx,
		state:    stateOS,
		sortMode: provider.SortDate,
		list:     newList("choose an operating system", items, 60, 18),
		spinner:  spin,
		progress: progress.New(progress.WithDefaultGradient()),
		sampler:  &sampler{},
		stopper:  &canceller{},
	}
}

func (m model) Init() tea.Cmd {
	if m.direct {
		return tea.Batch(m.startDownload(), tickCmd(), m.spinner.Tick)
	}
	return m.spinner.Tick
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.list.SetSize(msg.Width-4, msg.Height-6)
		m.progress.Width = min(msg.Width-8, 72)
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case releasesMsg:
		if msg.gen != m.gen {
			return m, nil // the user navigated away before this arrived
		}
		m.loading = false
		if msg.err != nil {
			m.err, m.state = msg.err, stateDone
			return m, nil
		}
		m.releases = msg.releases
		m.list = newList("", nil, m.listWidth(), m.listHeight(), sortHelpKeys()...)
		m = m.applySort("")
		m.state = stateReleases
		return m, nil

	case artifactMsg:
		if msg.gen != m.gen {
			return m, nil
		}
		m.loading = false
		if msg.err != nil {
			m.err, m.state = msg.err, stateDone
			return m, nil
		}
		m.artifact = msg.artifact
		m.destName = m.artifact.Target(m.cfg.OutDir).Dest
		m.state = stateDownloading
		return m, tea.Batch(m.startDownload(), tickCmd())

	case tickMsg:
		if m.state != stateDownloading {
			return m, nil
		}
		m.snapshot = m.sampler.get()
		return m, tickCmd()

	case doneMsg:
		m.state = stateDone
		m.outcome, m.err = msg.outcome, msg.err
		m.resumable = msg.err != nil && !errors.Is(msg.err, download.ErrUnverified)
		m.snapshot = m.sampler.get()
		if m.stopping {
			return m, tea.Quit // the engine has flushed its resume state by now
		}
		if msg.err == nil {
			// The job is done: leave the alt screen and print the result where
			// it can be read, copied and scrolled back to, rather than holding
			// the terminal to show something the user has finished with.
			return m, tea.Quit
		}
		return m, nil
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// While the filter prompt is open every key belongs to the list.
	if m.list.FilterState() == list.Filtering && m.state <= stateReleases {
		var cmd tea.Cmd
		m.list, cmd = m.list.Update(msg)
		return m, cmd
	}

	switch msg.String() {
	case "ctrl+c":
		if m.state == stateDownloading {
			// Stop the transfer and wait for it to flush its resume state
			// rather than killing the process mid-write.
			return m.stopDownload()
		}
		m.stopper.cancel()
		return m, tea.Quit

	case "q":
		if m.state == stateDownloading {
			return m.stopDownload()
		}
		return m, tea.Quit

	case "esc", "b", "backspace", "left":
		if m.state == stateDownloading {
			return m.stopDownload()
		}
		if m.canGoBack() {
			return m.goBack(), nil
		}
		if msg.String() == "left" {
			break // let the list page instead
		}
		return m, nil

	case "f", "right":
		if m.canGoForward() {
			return m.goForward(), nil
		}
		if msg.String() == "right" {
			break
		}
		return m, nil

	case "s":
		if m.state == stateReleases && !m.loading {
			m.sortMode = m.sortMode.Next()
			return m.applySort(m.selectedReleaseID()), nil
		}

	case "r":
		if m.state == stateReleases && !m.loading {
			m.sortDesc = !m.sortDesc
			return m.applySort(m.selectedReleaseID()), nil
		}

	case "enter":
		switch m.state {
		case stateOS:
			return m.chooseOS()
		case stateChannel:
			return m.chooseChannel()
		case stateFacet:
			return m.chooseFacet()
		case stateReleases:
			return m.chooseRelease()
		case stateDone:
			return m, tea.Quit
		}
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m model) chooseOS() (tea.Model, tea.Cmd) {
	item, ok := m.list.SelectedItem().(osItem)
	if !ok {
		return m, nil
	}
	if !item.available {
		m.status = item.name + " is not implemented yet"
		return m, nil
	}
	p, err := provider.Get(item.key)
	if err != nil {
		m.err, m.state = err, stateDone
		return m, nil
	}

	m = m.push()
	m.prov, m.provKey, m.status = p, item.key, ""
	channels := p.Channels()
	items := make([]list.Item, 0, len(channels))
	for _, c := range channels {
		items = append(items, channelItem{channel: c, osName: p.Name()})
	}
	m.list = newList(p.Name()+" - choose a channel", items, m.listWidth(), m.listHeight())
	m.state = stateChannel
	return m, nil
}

func (m model) chooseChannel() (tea.Model, tea.Cmd) {
	item, ok := m.list.SelectedItem().(channelItem)
	if !ok {
		return m, nil
	}
	m = m.push()
	m.channel = item.channel.ID
	m.allFacets = m.prov.Facets(m.channel)
	m.facets = provider.FacetsAt(m.allFacets, provider.StageList)
	m.facetAt = 0
	m.selection = provider.Selection{}

	if len(m.facets) > 0 {
		next, cmd := m.showFacet()
		return next, cmd
	}
	m.loading = true
	m.state = stateReleases
	return m, tea.Batch(m.loadReleases(), m.spinner.Tick)
}

// showFacet renders the current question. Each one is its own page, so back
// and forward walk through them like any other screen.
func (m model) showFacet() (model, tea.Cmd) {
	facet := m.facets[m.facetAt]

	items := make([]list.Item, 0, len(facet.Values)+1)
	if facet.AllowAll {
		items = append(items, facetItem{facet: facet, all: true})
	}
	for _, value := range facet.Values {
		items = append(items, facetItem{facet: facet, value: value})
	}

	m.list = newList(fmt.Sprintf("%s · %s - choose %s", m.prov.Name(), m.channel, strings.ToLower(facet.Label)),
		items, m.listWidth(), m.listHeight())
	m.state = stateFacet
	return m, nil
}

// chooseFacet records an answer and moves to the next question, or to the list
// once the provider has everything it asked for.
func (m model) chooseFacet() (tea.Model, tea.Cmd) {
	item, ok := m.list.SelectedItem().(facetItem)
	if !ok {
		return m, nil
	}

	m = m.push()
	if item.all {
		m.selection = m.selection.With(item.facet.Key, "")
	} else {
		m.selection = m.selection.With(item.facet.Key, item.value.ID)
	}

	m.facetAt++
	if m.facetAt < len(m.facets) {
		next, cmd := m.showFacet()
		return next, cmd
	}

	if item.facet.Stage == provider.StageResolve {
		// The questions about how to fetch are answered; go and fetch.
		m.loading = true
		return m, tea.Batch(m.resolveArtifact(), m.spinner.Tick)
	}

	m.loading = true
	m.state = stateReleases
	return m, tea.Batch(m.loadReleases(), m.spinner.Tick)
}

func (m model) chooseRelease() (tea.Model, tea.Cmd) {
	item, ok := m.list.SelectedItem().(releaseItem)
	if !ok {
		return m, nil
	}
	m = m.push()
	m.release = item.release

	if resolveFacets := provider.FacetsAt(m.allFacets, provider.StageResolve); len(resolveFacets) > 0 {
		m.facets = resolveFacets
		m.facetAt = 0
		next, cmd := m.showFacet()
		return next, cmd
	}

	m.loading = true
	return m, tea.Batch(m.resolveArtifact(), m.spinner.Tick)
}

// applySort reorders the release list in place, keeping the cursor on the same
// release so changing the order never loses the user's place. keepID may be
// empty, in which case the cursor goes to the top.
func (m model) applySort(keepID string) model {
	releases := append([]provider.Release(nil), m.releases...)
	provider.Sort(releases, m.sortMode, m.sortDesc)

	items := make([]list.Item, 0, len(releases))
	selected := 0
	for i, r := range releases {
		items = append(items, releaseItem{release: r})
		if keepID != "" && r.ID == keepID {
			selected = i
		}
	}

	m.list.Title = m.releaseListTitle()
	m.list.SetItems(items)
	m.list.Select(selected)
	return m
}

// releaseListTitle says what the list is sorted by, because an order the user
// cannot see is an order they cannot trust.
func (m model) releaseListTitle() string {
	arrow := "↓"
	if m.sortDesc {
		arrow = "↑"
	}
	scope := m.prov.Name() + " · " + m.channel
	if filters := m.selection.String(); filters != "" {
		scope += " · " + filters
	}
	return fmt.Sprintf("%s - choose a release · sort: %s %s", scope, m.sortMode.Short(), arrow)
}

func (m model) selectedReleaseID() string {
	if item, ok := m.list.SelectedItem().(releaseItem); ok {
		return item.release.ID
	}
	return ""
}

// sortHelpKeys advertises the sort controls in the list footer.
func sortHelpKeys() []key.Binding {
	return []key.Binding{
		key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "sort")),
		key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "reverse")),
	}
}

// stopDownload cancels the transfer and waits for the engine to finish writing
// its resume sidecar; the program quits when the download reports back.
// savedSummary is what a finished download leaves in the terminal: where the
// file is, and what was actually checked - a hash is worth little if it is
// only ever shown on a screen that then disappears.
func (m model) savedSummary() string {
	lines := []string{"saved " + m.outcome.Path}
	if m.outcome.AlreadyPresent {
		lines[0] = "already downloaded and verified: " + m.outcome.Path
	}

	report := m.outcome.Report
	if report == nil {
		lines = append(lines, "  verification skipped (--no-verify)")
		return strings.Join(lines, "\n")
	}

	for _, c := range report.Checks {
		marker := " "
		switch c.Status {
		case verify.Pass:
			marker = "✓"
		case verify.Fail:
			marker = "✗"
		case verify.Warn:
			marker = "!"
		}
		lines = append(lines, fmt.Sprintf("  %s %-10s %s", marker, c.Name, c.Detail))
	}
	if report.SHA256 != "" && !mentionsHash(report) {
		lines = append(lines, fmt.Sprintf("    %-10s %s", "sha256", report.SHA256))
	}
	if cmd := m.downloadCommand(); cmd != "" {
		lines = append(lines, "", "next time: "+cmd)
	}
	return strings.Join(lines, "\n")
}

// mentionsHash reports whether a check already printed the digest, so the
// summary does not repeat it.
func mentionsHash(report *verify.Report) bool {
	for _, c := range report.Checks {
		if strings.Contains(c.Detail, report.SHA256) {
			return true
		}
	}
	return false
}

// stopDownload asks the transfer to stop and waits for it to flush its resume
// state. Pressing the key again gives up waiting - a stuck engine must never
// trap the user in the UI.
func (m model) stopDownload() (model, tea.Cmd) {
	if m.stopping {
		return m, tea.Quit
	}
	m.stopping = true
	if !m.stopper.cancel() {
		return m, tea.Quit // nothing was running
	}
	return m, nil
}

// exitHint is what the terminal shows after the TUI closes: where a finished
// file went, or - for a download left unfinished - how to pick it up again.
func (m model) exitHint() string {
	if m.artifact.URL == "" {
		return ""
	}
	if m.outcome != nil && m.err == nil {
		return m.savedSummary()
	}

	snap := m.snapshot
	progress := "download stopped"
	if snap.Total > 0 && snap.Done > 0 {
		progress = fmt.Sprintf("download stopped at %.0f%% (%s of %s, kept in %s)",
			snap.Fraction()*100, humanBytes(snap.Done), humanBytes(snap.Total),
			m.destName+".part")
	}
	if cmd := m.downloadCommand(); cmd != "" {
		return progress + "\nresume with: " + cmd
	}
	return progress
}

func (m model) loadReleases() tea.Cmd {
	ctx, p, channel, sel, gen := m.ctx, m.prov, m.channel, m.selection, m.gen
	return func() tea.Msg {
		releases, err := p.List(ctx, channel, sel)
		return releasesMsg{gen: gen, releases: releases, err: err}
	}
}

func (m model) resolveArtifact() tea.Cmd {
	ctx, p, release, sel, gen := m.ctx, m.prov, m.release, m.selection, m.gen
	return func() tea.Msg {
		artifact, err := p.Resolve(ctx, release, sel)
		return artifactMsg{gen: gen, artifact: artifact, err: err}
	}
}

// startDownload runs the transfer under its own cancellable context so "q"
// stops the download without tearing down the program.
func (m model) startDownload() tea.Cmd {
	ctx, cancel := context.WithCancel(m.ctx)
	m.stopper.set(cancel)

	target := m.artifact.Target(m.cfg.OutDir)
	settings := download.Settings{
		Connections: m.cfg.Connections,
		LimitRate:   m.cfg.LimitRate,
		UserAgent:   ua.Resolve(m.cfg.UserAgent),
		SkipVerify:  m.cfg.SkipVerify,
		Logger:      m.logger,
	}
	outDir := m.cfg.OutDir
	sample := m.sampler.set

	return func() tea.Msg {
		if err := os.MkdirAll(outDir, 0o755); err != nil {
			return doneMsg{err: err}
		}
		outcome, err := download.Fetch(ctx, target, settings, sample)
		return doneMsg{outcome: outcome, err: err}
	}
}

func tickCmd() tea.Cmd {
	return tea.Tick(200*time.Millisecond, func(time.Time) tea.Msg { return tickMsg{} })
}

// downloadCommand is the command that fetches exactly this release without the
// picker. It names the OS, channel and every identifier, so the line can be
// pasted into a script or sent to someone else and still mean one specific
// file - the CLI requires all of them to match.
func (m model) downloadCommand() string {
	if m.release.ID == "" {
		return ""
	}

	args := []string{"osloader", "download"}
	if key := m.provKey; key != "" {
		args = append(args, "--os", key)
	}
	if m.channel != "" {
		args = append(args, "--channel", m.channel)
	}
	args = append(args, "--product", m.release.ID)
	if m.release.Version != "" {
		args = append(args, "--version", m.release.Version)
	}
	if m.release.Build != "" {
		args = append(args, "--build", m.release.Build)
	}
	for _, key := range sortedKeys(m.selection) {
		args = append(args, "--filter", key+"="+m.selection[key])
	}

	// Transfer settings are the local half: only worth repeating when they are
	// not the defaults the recipient would get anyway.
	if out := m.cfg.OutDir; out != "" && out != "." {
		args = append(args, "--out", quoteArg(out))
	}
	if m.cfg.Connections > 0 && m.cfg.Connections != download.DefaultConnections {
		args = append(args, "--connections", strconv.Itoa(m.cfg.Connections))
	}
	if m.cfg.LimitRate > 0 {
		args = append(args, "--limit-rate", strconv.FormatInt(m.cfg.LimitRate, 10))
	}
	if ua.Resolve(m.cfg.UserAgent) != ua.Default {
		args = append(args, "--user-agent", quoteArg(m.cfg.UserAgent))
	}
	if m.verbose {
		args = append(args, "--verbose")
	}
	return strings.Join(args, " ")
}

// sortedKeys keeps the printed command stable from run to run.
func sortedKeys(sel provider.Selection) []string {
	keys := make([]string, 0, len(sel))
	for key, value := range sel {
		if value != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

// quoteArg wraps a value in quotes only when the shell would need them.
func quoteArg(value string) string {
	if strings.ContainsAny(value, " \t\"'$") {
		return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
	}
	return value
}

func (m model) listWidth() int {
	if m.width > 4 {
		return m.width - 4
	}
	return 60
}

func (m model) listHeight() int {
	if m.height > 6 {
		return m.height - 6
	}
	return 18
}

func (m model) View() string {
	switch m.state {
	case stateDownloading:
		return m.viewDownload()
	case stateDone:
		return m.viewDone()
	}

	if m.loading {
		what := "catalog"
		if m.state == stateReleases && m.release.ID != "" {
			what = "download URL"
		}
		return bodyStyle.Render(fmt.Sprintf("%s fetching %s from %s…\n\n%s",
			m.spinner.View(), what, m.prov.Name(), dimStyle.Render("this takes a few seconds")))
	}

	view := m.list.View()
	if m.status != "" {
		view += "\n" + warnStyle.Render("  "+m.status)
	}
	return view
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
