package macos

import (
	"strings"
	"testing"

	"github.com/ryanparsa/osloader/internal/provider"
)

func release(version, build string) provider.Release {
	return provider.Release{Version: version, Build: build}
}

// TestParseDist extracts what the catalog does not carry, and is not fooled by
// the CDATA script block that every real distribution contains.
func TestParseDist(t *testing.T) {
	info := parseDist(readFixture(t, "sample.dist"))

	if info.Title != "macOS 27 Golden Gate" {
		t.Errorf("title = %q", info.Title)
	}
	if info.Version != "27.0" {
		t.Errorf("version = %q, want 27.0", info.Version)
	}
	if info.Build != "26A428" {
		t.Errorf("build = %q, want 26A428 (99Z999 would mean the CDATA script won)", info.Build)
	}
}

// TestParseDistFallsBackToVersStr covers products with no auxinfo block.
func TestParseDistFallsBackToVersStr(t *testing.T) {
	info := parseDist(readFixture(t, "noaux.dist"))

	if info.Title != "macOS Sequoia" {
		t.Errorf("title = %q", info.Title)
	}
	if info.Version != "15.8" {
		t.Errorf("version = %q, want the versStr fallback 15.8", info.Version)
	}
	if info.Build != "" {
		t.Errorf("build = %q, want empty when the file does not say", info.Build)
	}
}

// TestArtifactName keeps downloaded files identifiable in a busy folder.
func TestArtifactName(t *testing.T) {
	name := artifactName(release("27.0", "26A428"), "InstallAssistant.pkg")
	if name != "macOS_27.0_26A428_InstallAssistant.pkg" {
		t.Errorf("name = %q", name)
	}
	if got := artifactName(release("", ""), "InstallAssistant.pkg"); got != "InstallAssistant.pkg" {
		t.Errorf("unknown version/build name = %q", got)
	}
	// A hostile catalog must not be able to steer the file out of the download
	// directory through the version or build string.
	got := artifactName(release("27.0/../../etc", "26A428"), "x.pkg")
	if strings.ContainsAny(got, `/\`) || strings.HasPrefix(got, "..") {
		t.Errorf("name %q escapes the download directory", got)
	}
	if !strings.HasSuffix(got, "_x.pkg") {
		t.Errorf("name %q lost the base file name", got)
	}
}
