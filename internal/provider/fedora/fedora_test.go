package fedora

import (
	"context"
	"testing"

	"github.com/ryanparsa/osloader/internal/provider"
)

// offline returns a provider with a fixed catalogue, so tests never reach for
// the network.
func offline(releases ...release) *Provider {
	p := New()
	p.loaded = true
	p.releases = releases
	return p
}

var catalogue = []release{
	{Version: "44", Arch: "x86_64", Variant: "Workstation", Subvariant: "Workstation", SHA256: "aa", Size: "2981865472",
		Link: "https://download.fedoraproject.org/pub/fedora/linux/releases/44/Workstation/x86_64/iso/Fedora-Workstation-Live-44-1.4.x86_64.iso"},
	{Version: "44", Arch: "aarch64", Variant: "KDE", Subvariant: "KDE Desktop", SHA256: "bb", Size: "3221225472",
		Link: "https://download.fedoraproject.org/pub/fedora/linux/releases/44/KDE/aarch64/iso/Fedora-KDE-Live-44-1.4.aarch64.iso"},
	{Version: "45 Beta", Arch: "x86_64", Variant: "Workstation", Subvariant: "Workstation", SHA256: "cc", Size: "3000000000",
		Link: "https://download.fedoraproject.org/pub/fedora/linux/releases/test/45_Beta/Workstation/x86_64/iso/Fedora-Workstation-Live-45_Beta-1.3.x86_64.iso"},
}

// TestChannelsSplitStableFromBeta keeps test builds out of the way of someone
// installing Fedora on a real machine.
func TestChannelsSplitStableFromBeta(t *testing.T) {
	p := offline(catalogue...)

	stable, err := p.List(context.Background(), "stable", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(stable) != 2 {
		t.Errorf("stable has %d images, want the two released ones", len(stable))
	}
	for _, r := range stable {
		if r.Version == "45 Beta" {
			t.Error("a beta appeared in the stable channel")
		}
	}

	beta, err := p.List(context.Background(), "prerelease", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(beta) != 1 || beta[0].Version != "45 Beta" {
		t.Errorf("prerelease = %+v", beta)
	}
}

// TestListCarriesSizeAndTitle: Fedora publishes the size, so it is shown and
// later checked rather than guessed.
func TestListCarriesSizeAndTitle(t *testing.T) {
	releases, err := offline(catalogue...).List(context.Background(), "stable", provider.Selection{"arch": "aarch64"})
	if err != nil {
		t.Fatal(err)
	}
	if len(releases) != 1 {
		t.Fatalf("got %d releases, want the one aarch64 image", len(releases))
	}

	r := releases[0]
	if r.ID != "Fedora-KDE-Live-44-1.4.aarch64.iso" {
		t.Errorf("id = %q", r.ID)
	}
	if r.Size != 3221225472 {
		t.Errorf("size = %d", r.Size)
	}
	if r.Title != "Fedora KDE KDE Desktop · aarch64" {
		t.Errorf("title = %q", r.Title)
	}
}

// TestFacetsComeFromTheCatalogue: the menu offers what Fedora actually built
// for that channel, not a list written here.
func TestFacetsComeFromTheCatalogue(t *testing.T) {
	p := offline(catalogue...)

	facets := p.Facets("stable")
	if len(facets) != 2 {
		t.Fatalf("got %d facets", len(facets))
	}

	arches := facets[0]
	if arches.Key != "arch" || len(arches.Values) != 2 {
		t.Errorf("architectures = %+v", arches.Values)
	}
	if arches.Values[0].ID != "aarch64" || arches.Values[0].Label != "aarch64 - 64-bit ARM" {
		t.Errorf("first architecture = %+v", arches.Values[0])
	}

	variants := facets[1]
	if variants.Key != "variant" || len(variants.Values) != 2 {
		t.Errorf("variants = %+v", variants.Values)
	}

	// The beta channel is built from its own entries.
	if beta := p.Facets("prerelease"); len(beta[0].Values) != 1 || beta[0].Values[0].ID != "x86_64" {
		t.Errorf("prerelease architectures = %+v", beta[0].Values)
	}
}

// TestResolveUsesThePublishedDigestAndSize: Fedora's redirector sends the
// request to a mirror, so both checks have to come from the catalogue.
func TestResolveUsesThePublishedDigestAndSize(t *testing.T) {
	p := offline(catalogue...)
	releases, err := p.List(context.Background(), "stable", provider.Selection{"arch": "x86_64"})
	if err != nil {
		t.Fatal(err)
	}

	artifact, err := p.Resolve(context.Background(), releases[0], nil)
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Digest != "aa" || artifact.Size != 2981865472 {
		t.Errorf("artifact = %+v", artifact)
	}
	if artifact.Verifier == nil {
		t.Error("a Fedora download must be checked against its digest")
	}
	if artifact.MaxPerHost != mirrorConnections {
		t.Errorf("MaxPerHost = %d, want the mirror limit", artifact.MaxPerHost)
	}
	if artifact.Filename != "Fedora-Workstation-Live-44-1.4.x86_64.iso" {
		t.Errorf("filename = %q", artifact.Filename)
	}

	// A release that is not in the catalogue is an error, not a guess.
	if _, err := p.Resolve(context.Background(), provider.Release{ID: "nope.iso", Channel: "stable"}, nil); err == nil {
		t.Error("an unknown release should be reported")
	}
}

func TestPrereleaseDetection(t *testing.T) {
	cases := map[string]bool{"44": false, "45 Beta": true, "45_Beta": true, "43": false}
	for version, want := range cases {
		if got := (release{Version: version}).prerelease(); got != want {
			t.Errorf("prerelease(%q) = %v, want %v", version, got, want)
		}
	}
}
