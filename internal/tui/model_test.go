package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ryanparsa/osloader/internal/download"
	"github.com/ryanparsa/osloader/internal/provider"
	"github.com/ryanparsa/osloader/internal/verify"

	_ "github.com/ryanparsa/osloader/internal/provider/debian"
	_ "github.com/ryanparsa/osloader/internal/provider/macos"
	_ "github.com/ryanparsa/osloader/internal/provider/windows"
)

// sizedWith returns a model that has been told how big the terminal is, which
// is what the real program gets from its first WindowSizeMsg.
func sizedWith(t *testing.T, cfg Config) model {
	t.Helper()
	m := newModel(context.Background(), cfg)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	return updated.(model)
}

func sized(t *testing.T) model {
	t.Helper()
	return sizedWith(t, Config{OutDir: t.TempDir(), Connections: 4})
}

func keyMsg(s string) tea.KeyMsg {
	if s == "enter" {
		return tea.KeyMsg{Type: tea.KeyEnter}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

// selectItem moves the cursor onto the item whose rendered title contains want.
func selectItem(t *testing.T, m model, want string) model {
	t.Helper()
	for i, item := range m.list.Items() {
		if titled, ok := item.(interface{ Title() string }); ok && strings.Contains(titled.Title(), want) {
			m.list.Select(i)
			return m
		}
	}
	t.Fatalf("no item containing %q in %d items", want, len(m.list.Items()))
	return m
}

// TestPickerWalksOSThenChannel covers the first two screens without touching
// the network: choosing a channel only returns the command that would fetch.
func TestPickerWalksOSThenChannel(t *testing.T) {
	m := sized(t)
	if !strings.Contains(m.View(), "choose an operating system") {
		t.Fatalf("first screen does not offer an OS:\n%s", m.View())
	}

	m = selectItem(t, m, "macOS")
	next, _ := m.Update(keyMsg("enter"))
	m = next.(model)
	if m.state != stateChannel {
		t.Fatalf("state = %v after choosing macOS, want stateChannel", m.state)
	}
	if view := m.View(); !strings.Contains(view, "Public") {
		t.Errorf("channel screen missing the public channel:\n%s", view)
	}

	next, cmd := m.Update(keyMsg("enter"))
	m = next.(model)
	if m.state != stateReleases || !m.loading {
		t.Errorf("choosing a channel should start loading releases (state=%v loading=%v)", m.state, m.loading)
	}
	if cmd == nil {
		t.Error("choosing a channel returned no command to fetch the catalog")
	}
	if !strings.Contains(m.View(), "fetching catalog") {
		t.Errorf("loading screen does not say what it is doing:\n%s", m.View())
	}
}

// TestUnavailableOSIsExplained: the stubs must say they are coming, not fail.
func TestUnavailableOSIsExplained(t *testing.T) {
	m := selectItem(t, sized(t), "Windows")
	next, _ := m.Update(keyMsg("enter"))
	m = next.(model)

	if m.state != stateOS {
		t.Errorf("state = %v, want to stay on the OS picker", m.state)
	}
	if !strings.Contains(m.View(), "not implemented yet") {
		t.Errorf("no explanation shown:\n%s", m.View())
	}
}

// TestReleaseListRendersDetail checks the release rows carry what a person
// needs to choose: version, build, date and size.
func TestReleaseListRendersDetail(t *testing.T) {
	m := selectItem(t, sized(t), "macOS")
	next, _ := m.Update(keyMsg("enter"))
	m = next.(model)
	next, _ = m.Update(keyMsg("enter"))
	m = next.(model)

	posted := time.Date(2026, 9, 14, 17, 13, 11, 0, time.UTC)
	next, _ = m.Update(releasesMsg{gen: m.gen, releases: []provider.Release{{
		ID: "142-15488", Version: "27.0", Build: "26A428",
		Title: "macOS 27 Golden Gate", Posted: posted, Size: 18400314350,
	}}})
	m = next.(model)

	view := m.View()
	for _, want := range []string{"27.0", "macOS 27 Golden Gate", "26A428", "17.1 GiB", "142-15488"} {
		if !strings.Contains(view, want) {
			t.Errorf("release list missing %q:\n%s", want, view)
		}
	}
}

// TestDownloadViewShowsProgress renders the transfer screen from a snapshot.
func TestDownloadViewShowsProgress(t *testing.T) {
	m := sized(t)
	m.state = stateDownloading
	m.artifact = provider.Artifact{URL: "https://swcdn.apple.com/x/InstallAssistant.pkg"}
	m.destName = "/tmp/InstallAssistant.pkg"
	m.snapshot = download.Snapshot{
		Total: 18400314350, Done: 9200157175, Speed: 11 << 20,
		ETA: 13 * time.Minute,
		Workers: []download.WorkerSnapshot{
			{Bytes: 2300039293, Speed: 3 << 20, Chunk: 0, ChunkDone: 2300039293, ChunkTotal: 4600078587},
			{Bytes: 2300039294, Speed: 3 << 20, Chunk: 1, ChunkDone: 2300039294, ChunkTotal: 4600078587},
			{Bytes: 2300039294, Speed: 3 << 20, Chunk: 2, ChunkDone: 2300039294, ChunkTotal: 4600078588},
			{Bytes: 2300039294, Speed: 2 << 20, Chunk: -1},
		},
	}

	view := m.View()
	for _, want := range []string{"8.6 GiB", "17.1 GiB", "11.0 MiB/s", "13m00s", "connections", "q cancels"} {
		if !strings.Contains(view, want) {
			t.Errorf("download view missing %q:\n%s", want, view)
		}
	}
}

// TestDoneViewReportsChecks shows the verification result, check by check.
func TestDoneViewReportsChecks(t *testing.T) {
	m := sized(t)
	next, _ := m.Update(doneMsg{outcome: &download.Outcome{
		Path: "/tmp/macOS_27.0_26A428_InstallAssistant.pkg",
		Report: &verify.Report{Checks: []verify.Check{
			{Name: "size", Status: verify.Pass, Detail: "matches the catalog"},
			{Name: "signature", Status: verify.Pass, Detail: "Apple Root CA chain, trusted by macOS"},
		}},
	}})
	m = next.(model)

	if m.state != stateDone {
		t.Fatalf("state = %v, want stateDone", m.state)
	}
	view := m.View()
	for _, want := range []string{"verified", "signature", "Apple Root CA", "macOS_27.0_26A428"} {
		if !strings.Contains(view, want) {
			t.Errorf("result view missing %q:\n%s", want, view)
		}
	}
}

// TestFailedDownloadOffersResume: an interrupted transfer must tell the user it
// can be picked up again rather than implying the work is lost.
func TestFailedDownloadOffersResume(t *testing.T) {
	m := sized(t)
	next, _ := m.Update(doneMsg{err: context.Canceled})
	m = next.(model)

	view := m.View()
	if !strings.Contains(view, "resume") {
		t.Errorf("cancelled download does not mention resuming:\n%s", view)
	}
}

// releaseScreen walks to the release list and fills it with known releases.
func releaseScreen(t *testing.T) model {
	t.Helper()
	m := selectItem(t, sized(t), "macOS")
	next, _ := m.Update(keyMsg("enter"))
	next, _ = next.(model).Update(keyMsg("enter"))

	posted := func(d int) time.Time { return time.Date(2026, 9, d, 0, 0, 0, 0, time.UTC) }
	// The catalog reply has to carry the generation the model is waiting for,
	// exactly as the real command does.
	next, _ = next.(model).Update(releasesMsg{gen: next.(model).gen, releases: []provider.Release{
		{ID: "tahoe", Version: "26.7", Title: "macOS Tahoe", Posted: posted(14), Size: 17 << 30},
		{ID: "gate", Version: "27.0", Title: "macOS Golden Gate", Posted: posted(14), Size: 18 << 30},
		{ID: "bigsur", Version: "11.7.11", Title: "macOS Big Sur", Posted: posted(2), Size: 11 << 30},
	}})
	return next.(model)
}

func topRelease(t *testing.T, m model) string {
	t.Helper()
	item, ok := m.list.Items()[0].(releaseItem)
	if !ok {
		t.Fatalf("first item is not a release: %T", m.list.Items()[0])
	}
	return item.release.ID
}

// TestSortCyclesAndShowsMode: "s" changes the order and says so in the header.
func TestSortCyclesAndShowsMode(t *testing.T) {
	m := releaseScreen(t)
	if got := topRelease(t, m); got != "gate" {
		t.Fatalf("default order starts with %q, want the newest (gate)", got)
	}
	if !strings.Contains(m.list.Title, "sort: date ↓") {
		t.Errorf("header does not show the sort: %q", m.list.Title)
	}

	// date → version → size → name, back to date.
	wantTops := map[provider.SortMode]string{
		provider.SortVersion: "gate",
		provider.SortSize:    "gate",
		provider.SortName:    "bigsur", // alphabetical: Big Sur first
	}
	for _, mode := range []provider.SortMode{provider.SortVersion, provider.SortSize, provider.SortName} {
		next, _ := m.Update(keyMsg("s"))
		m = next.(model)
		if m.sortMode != mode {
			t.Fatalf("after s: mode = %q, want %q", m.sortMode, mode)
		}
		if !strings.Contains(m.list.Title, "sort: "+string(mode)) {
			t.Errorf("header %q does not name the %s sort", m.list.Title, mode)
		}
		if got := topRelease(t, m); got != wantTops[mode] {
			t.Errorf("%s sort starts with %q, want %q", mode, got, wantTops[mode])
		}
	}

	next, _ := m.Update(keyMsg("s"))
	if m = next.(model); m.sortMode != provider.SortDate {
		t.Errorf("sorting did not cycle back to date, got %q", m.sortMode)
	}
}

// TestReverseKeepsSelection: flipping the order must not move the cursor off
// the release the user had highlighted.
func TestReverseKeepsSelection(t *testing.T) {
	m := releaseScreen(t)
	m.list.Select(2) // the oldest release
	selected := m.selectedReleaseID()

	next, _ := m.Update(keyMsg("r"))
	m = next.(model)

	if !m.sortDesc {
		t.Fatal("r did not reverse the order")
	}
	if !strings.Contains(m.list.Title, "↑") {
		t.Errorf("header does not show the reversed direction: %q", m.list.Title)
	}
	if got := topRelease(t, m); got != selected {
		t.Errorf("reversed list starts with %q, want the previously oldest %q", got, selected)
	}
	if m.selectedReleaseID() != selected {
		t.Errorf("cursor moved to %q, want to stay on %q", m.selectedReleaseID(), selected)
	}
}

// TestBackAndForwardReturnToTheSamePage: navigation must restore the page as
// it was, cursor included, in both directions.
func TestBackAndForwardReturnToTheSamePage(t *testing.T) {
	m := releaseScreen(t)
	m.list.Select(2)
	selected := m.selectedReleaseID()

	// releases -> channels -> os
	back, _ := m.Update(keyMsg("b"))
	m = back.(model)
	if m.state != stateChannel {
		t.Fatalf("back from the release list went to %v, want stateChannel", m.state)
	}
	back, _ = m.Update(keyMsg("b"))
	m = back.(model)
	if m.state != stateOS {
		t.Fatalf("back from channels went to %v, want stateOS", m.state)
	}
	if again, _ := m.Update(keyMsg("b")); again.(model).state != stateOS {
		t.Error("back from the first page should stay put")
	}

	// forward all the way again
	fwd, _ := m.Update(keyMsg("f"))
	m = fwd.(model)
	if m.state != stateChannel {
		t.Fatalf("forward went to %v, want stateChannel", m.state)
	}
	fwd, _ = m.Update(keyMsg("f"))
	m = fwd.(model)
	if m.state != stateReleases {
		t.Fatalf("forward went to %v, want stateReleases", m.state)
	}
	if m.selectedReleaseID() != selected {
		t.Errorf("forward lost the cursor: on %q, want %q", m.selectedReleaseID(), selected)
	}
	if len(m.releases) == 0 {
		t.Error("forward lost the loaded releases")
	}
}

// TestNavigatingAwayIgnoresLateReplies: a catalog that arrives after the user
// has gone back must not yank them forward again.
func TestNavigatingAwayIgnoresLateReplies(t *testing.T) {
	m := selectItem(t, sized(t), "macOS")
	next, _ := m.Update(keyMsg("enter")) // channels
	m = next.(model)
	staleGen := m.gen

	next, _ = m.Update(keyMsg("enter")) // starts loading releases
	m = next.(model)
	next, _ = m.Update(keyMsg("b")) // user changes their mind
	m = next.(model)

	if m.state != stateChannel {
		t.Fatalf("state = %v after going back, want stateChannel", m.state)
	}

	next, _ = m.Update(releasesMsg{gen: staleGen, releases: []provider.Release{{ID: "late"}}})
	m = next.(model)
	if m.state != stateChannel {
		t.Errorf("a late catalog reply moved the user to %v", m.state)
	}
}

// TestNewChoiceDropsTheForwardStack mirrors browser behaviour.
func TestNewChoiceDropsTheForwardStack(t *testing.T) {
	m := releaseScreen(t)
	back, _ := m.Update(keyMsg("b"))
	m = back.(model)
	if !m.canGoForward() {
		t.Fatal("going back should leave something to go forward to")
	}

	next, _ := m.Update(keyMsg("enter")) // choose a channel again
	m = next.(model)
	if m.canGoForward() {
		t.Error("choosing a new page should discard the forward history")
	}
}

// TestExitHintAfterAnUnfinishedDownload is what the user sees in their shell
// once the alt screen is gone.
func TestExitHintAfterAnUnfinishedDownload(t *testing.T) {
	m := downloadingWith(t, Config{OutDir: ".", Connections: download.DefaultConnections}, sampleWorkers())
	m.snapshot.Done = 6012954214
	m.snapshot.Total = 18400314350

	hint := m.exitHint()
	for _, want := range []string{"33%", "5.6 GiB", "17.1 GiB", ".part", "resume with:", "--build 26A428"} {
		if !strings.Contains(hint, want) {
			t.Errorf("exit hint missing %q:\n%s", want, hint)
		}
	}

	// A finished download says where the file went instead.
	m.outcome = &download.Outcome{Path: "./macOS_27.0_26A428_InstallAssistant.pkg"}
	if got := m.exitHint(); !strings.HasPrefix(got, "saved ./macOS_27.0_26A428_InstallAssistant.pkg") {
		t.Errorf("finished hint = %q", got)
	}

	// Nothing was ever started: nothing to say.
	if got := sized(t).exitHint(); got != "" {
		t.Errorf("hint without a download = %q", got)
	}
}

// TestStoppingWaitsForTheEngine: q must not kill the process before the resume
// sidecar is written - the model waits for the download to report back.
func TestStoppingWaitsForTheEngine(t *testing.T) {
	m := downloadingWith(t, Config{OutDir: ".", Connections: download.DefaultConnections}, sampleWorkers())
	cancelled := false
	m.stopper.set(func() { cancelled = true })

	next, cmd := m.Update(keyMsg("q"))
	m = next.(model)
	if !cancelled {
		t.Error("q did not cancel the transfer")
	}
	if !m.stopping {
		t.Error("q did not put the model into stopping")
	}
	if cmd != nil {
		t.Error("q should wait for the engine, not quit immediately")
	}
	if !strings.Contains(visible(m.View()), "stopping") {
		t.Error("the screen does not say it is stopping")
	}

	next, cmd = m.Update(doneMsg{err: context.Canceled})
	if cmd == nil {
		t.Error("the model should quit once the download has reported back")
	}
	if next.(model).state != stateDone {
		t.Error("state should be stateDone after the download stops")
	}
}

// TestCancelSurvivesModelCopies is the regression test for Ctrl-C doing
// nothing: Bubble Tea runs Init on a copy of the model, so the cancel function
// has to live somewhere shared rather than in a field of that copy.
func TestCancelSurvivesModelCopies(t *testing.T) {
	cancelled := make(chan struct{})
	m := sizedWith(t, Config{OutDir: t.TempDir(), Connections: 2})
	m.direct = true
	m.state = stateDownloading
	m.artifact = provider.Artifact{URL: "https://example.invalid/file.iso", Filename: "file.iso"}
	m.ctx = context.Background()

	// Init returns the command that starts the transfer - and runs on a copy.
	if cmd := m.Init(); cmd == nil {
		t.Fatal("direct mode did not start a download")
	}
	m.stopper.set(func() { close(cancelled) })

	stopped, _ := m.Update(keyMsg("ctrl+c"))
	if !stopped.(model).stopping {
		t.Error("ctrl+c did not put the model into stopping")
	}
	select {
	case <-cancelled:
	default:
		t.Fatal("ctrl+c did not reach the running download")
	}

	// A second press gives up waiting rather than trapping the user.
	if _, cmd := stopped.(model).Update(keyMsg("ctrl+c")); cmd == nil {
		t.Error("a second ctrl+c should quit immediately")
	}
}

// TestEachOSAsksItsOwnQuestions: Debian needs an architecture and an image
// size, macOS needs neither, and the picker adapts rather than forcing one
// shape on both.
func TestEachOSAsksItsOwnQuestions(t *testing.T) {
	m := selectItem(t, sized(t), "Debian")
	next, _ := m.Update(keyMsg("enter")) // channels
	m = next.(model)
	if m.state != stateChannel {
		t.Fatalf("state = %v, want stateChannel", m.state)
	}

	next, _ = m.Update(keyMsg("enter")) // first channel -> first question
	m = next.(model)
	if m.state != stateFacet {
		t.Fatalf("Debian went to %v instead of asking its first question", m.state)
	}
	if !strings.Contains(m.list.Title, "architecture") {
		t.Errorf("first question is %q, want the architecture", m.list.Title)
	}

	m = selectItem(t, m, "amd64")
	next, _ = m.Update(keyMsg("enter"))
	m = next.(model)
	if m.state != stateFacet || !strings.Contains(m.list.Title, "image size") {
		t.Fatalf("second question is %q (state %v), want the image size", m.list.Title, m.state)
	}
	if m.selection.Get("arch") != "amd64" {
		t.Errorf("selection = %v, want arch=amd64", m.selection)
	}

	m = selectItem(t, m, "Any image size")
	next, _ = m.Update(keyMsg("enter"))
	m = next.(model)
	if m.state != stateReleases || !m.loading {
		t.Fatalf("after the last question: state %v loading %v, want the release list to load", m.state, m.loading)
	}
	if m.selection.Get("media") != "" {
		t.Errorf("\"any\" should leave the facet unset, got %q", m.selection.Get("media"))
	}

	// Those answers belong in the shareable command (with the local-only
	// settings left at their defaults so the line stays portable).
	m.release = provider.Release{ID: "debian-13.7.0-amd64-netinst.iso", Version: "13.7.0"}
	m.provKey = "debian"
	m.cfg.OutDir = "."
	m.cfg.Connections = download.DefaultConnections
	if got, want := m.downloadCommand(),
		"osloader download --os debian --channel stable --product debian-13.7.0-amd64-netinst.iso --version 13.7.0 --filter arch=amd64"; got != want {
		t.Errorf("command = %q, want %q", got, want)
	}

	// Each question is a page: back walks out of them one at a time.
	for _, want := range []state{stateFacet, stateFacet, stateChannel} {
		back, _ := m.Update(keyMsg("b"))
		m = back.(model)
		if m.state != want {
			t.Fatalf("back landed on %v, want %v", m.state, want)
		}
	}
}

// TestMacOSSkipsStraightToTheList is the other half: no facets, no extra page.
func TestMacOSSkipsStraightToTheList(t *testing.T) {
	m := selectItem(t, sized(t), "macOS")
	next, _ := m.Update(keyMsg("enter"))
	next, _ = next.(model).Update(keyMsg("enter"))
	m = next.(model)

	if m.state != stateReleases {
		t.Errorf("macOS went to %v, want straight to the release list", m.state)
	}
	if len(m.facets) != 0 {
		t.Errorf("macOS declared %d facets", len(m.facets))
	}
}

// TestFinishedDirectDownloadExitsWithTheDetails: after `osloader download`
// there is nothing left to choose, so the UI gets out of the way and leaves
// the path and what was verified in the terminal.
func TestFinishedDirectDownloadExitsWithTheDetails(t *testing.T) {
	m := downloadingWith(t, Config{OutDir: ".", Connections: download.DefaultConnections}, sampleWorkers())
	m.direct = true

	next, cmd := m.Update(doneMsg{outcome: &download.Outcome{
		Path: "./debian-13.7.0-arm64-netinst.iso",
		Report: &verify.Report{
			SHA256: "6e93fa1759bd9d4b0fc11e938987de6967ee7de5297dac1be27c3a75cc17024b",
			Checks: []verify.Check{
				{Name: "size", Status: verify.Info, Detail: "735358976 bytes"},
				{Name: "sha256", Status: verify.Pass, Detail: "matches Debian's signed SHA256SUMS"},
			},
		},
	}})
	m = next.(model)

	if cmd == nil {
		t.Error("a finished direct download should leave the UI on its own")
	}

	summary := m.exitHint()
	for _, want := range []string{
		"saved ./debian-13.7.0-arm64-netinst.iso",
		"✓ sha256",
		"matches Debian's signed SHA256SUMS",
		"6e93fa1759bd9d4b0fc11e938987de6967ee7de5297dac1be27c3a75cc17024b",
	} {
		if !strings.Contains(summary, want) {
			t.Errorf("exit summary missing %q:\n%s", want, summary)
		}
	}

	// In the picker the user may want to fetch something else, so it stays.
	picker := downloadingWith(t, Config{OutDir: ".", Connections: download.DefaultConnections}, sampleWorkers())
	if _, cmd := picker.Update(doneMsg{outcome: &download.Outcome{Path: "./x.iso"}}); cmd != nil {
		t.Error("the picker should stay open after a download")
	}
}
