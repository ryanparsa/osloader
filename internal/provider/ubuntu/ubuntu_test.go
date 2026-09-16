package ubuntu

import (
	"strings"
	"testing"

	"github.com/ryanparsa/osloader/internal/provider"
	"github.com/ryanparsa/osloader/internal/provider/webdir"
)

// TestDescribe reads what an Ubuntu file name says about the image, including
// the "live-server" naming and the suffixed builds.
func TestDescribe(t *testing.T) {
	cases := map[string]imageName{
		"ubuntu-24.04.5.1-desktop-amd64.iso":            {version: "24.04.5.1", variant: "desktop", arch: "amd64"},
		"ubuntu-24.04.3-live-server-arm64.iso":          {version: "24.04.3", variant: "server", arch: "arm64"},
		"ubuntu-24.04.3-live-server-arm64+largemem.iso": {version: "24.04.3", variant: "server", arch: "arm64+largemem"},
		"ubuntu-25.10-desktop-amd64.iso":                {version: "25.10", variant: "desktop", arch: "amd64"},
		"ubuntu-24.04.3-live-server-riscv64.iso":        {version: "24.04.3", variant: "server", arch: "riscv64"},
	}
	for filename, want := range cases {
		got, ok := describe(filename)
		if !ok {
			t.Errorf("describe(%s) did not recognise the name", filename)
			continue
		}
		if got != want {
			t.Errorf("describe(%s) = %+v, want %+v", filename, got, want)
		}
	}

	// Anything that is not an installer image is ignored.
	for _, other := range []string{
		"ubuntu-24.04.3-desktop-amd64.iso.torrent",
		"ubuntu-24.04.3-wsl-amd64.wsl",
		"SHA256SUMS",
	} {
		if _, ok := describe(other); ok {
			t.Errorf("describe(%s) should not match an installer image", other)
		}
	}
}

// TestIsLTS: Canonical ships an LTS every second April, and the channel relies
// on spotting them from the version alone.
func TestIsLTS(t *testing.T) {
	lts := []string{"24.04", "24.04.5", "22.04.4", "26.04"}
	interim := []string{"25.10", "23.10", "25.04", "24.10"}

	for _, v := range lts {
		if !isLTS(v) {
			t.Errorf("%s should be an LTS", v)
		}
	}
	for _, v := range interim {
		if isLTS(v) {
			t.Errorf("%s is an interim release, not an LTS", v)
		}
	}
}

// TestDirectoriesFollowTheArchitecture: amd64 lives on releases.ubuntu.com and
// everything else on cdimage, so naming an architecture halves the requests.
func TestDirectoriesFollowTheArchitecture(t *testing.T) {
	cases := map[string][]string{
		"":      {releasesRoot + "24.04/", cdimageRoot + "24.04/release/"},
		"amd64": {releasesRoot + "24.04/"},
		"arm64": {cdimageRoot + "24.04/release/"},
	}
	for arch, want := range cases {
		got := directories("24.04", arch)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("directories(24.04, %q) = %v, want %v", arch, got, want)
		}
	}
}

// TestPairImagesKeepsInstallersOnly: the checksum file covers torrents, WSL
// bundles and manifests too, none of which are installable media.
func TestPairImagesKeepsInstallersOnly(t *testing.T) {
	sums := webdir.Checksums{
		"ubuntu-24.04.5.1-desktop-amd64.iso":       "aa",
		"ubuntu-24.04.5-live-server-amd64.iso":     "bb",
		"ubuntu-24.04.3-wsl-amd64.wsl":             "cc",
		"ubuntu-24.04.3-desktop-amd64.iso.torrent": "dd",
	}
	listed := map[string]webdir.File{
		"ubuntu-24.04.5.1-desktop-amd64.iso": {Size: 6 << 30},
	}

	images := pairImages(sums, listed, releasesRoot+"24.04/")
	if len(images) != 2 {
		t.Fatalf("kept %d images, want the two ISOs: %v", len(images), images)
	}

	desktop := images["ubuntu-24.04.5.1-desktop-amd64.iso"]
	if desktop.variant != "desktop" || desktop.arch != "amd64" || desktop.sha256 != "aa" {
		t.Errorf("desktop image = %+v", desktop)
	}
	if desktop.url != releasesRoot+"24.04/ubuntu-24.04.5.1-desktop-amd64.iso" {
		t.Errorf("url = %q", desktop.url)
	}
	if desktop.file.Size != 6<<30 {
		t.Errorf("size = %d, want the listed size", desktop.file.Size)
	}
	if images["ubuntu-24.04.5-live-server-amd64.iso"].variant != "server" {
		t.Error("live-server should be presented as server")
	}
}

func TestMatches(t *testing.T) {
	img := image{arch: "arm64", variant: "server"}
	cases := map[string]bool{
		"":                true,
		"arch=arm64":      true,
		"arch=amd64":      false,
		"variant=server":  true,
		"variant=desktop": false,
	}
	for filter, want := range cases {
		sel := provider.Selection{}
		if filter != "" {
			key, value, _ := strings.Cut(filter, "=")
			sel = sel.With(key, value)
		}
		if got := matches(img, sel); got != want {
			t.Errorf("matches(%q) = %v, want %v", filter, got, want)
		}
	}
}

// TestFacets: Ubuntu asks for an architecture and an edition, and nothing after
// a release is chosen - Canonical serves the images itself.
func TestFacets(t *testing.T) {
	p := New()
	list := provider.FacetsAt(p.Facets("lts"), provider.StageList)
	if len(list) != 2 || list[0].Key != "arch" || list[1].Key != "variant" {
		t.Errorf("list questions = %+v", list)
	}
	if got := provider.FacetsAt(p.Facets("lts"), provider.StageResolve); len(got) != 0 {
		t.Errorf("Ubuntu asks %d questions after choosing a release, want none", len(got))
	}
	if err := provider.Validate(p.Facets("lts"), provider.Selection{"arch": "sparc"}); err == nil {
		t.Error("an unknown architecture should be rejected")
	}
}
