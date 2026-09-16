package debian

import (
	"context"
	"strings"
	"testing"

	"github.com/ryanparsa/osloader/internal/provider"
	"github.com/ryanparsa/osloader/internal/provider/webdir"
)

// TestDirectoriesFollowTheSelection: choosing an architecture or an image size
// should also mean fetching fewer indexes.
func TestDirectoriesFollowTheSelection(t *testing.T) {
	cases := []struct {
		channel, arch, media string
		want                 []string
	}{
		{"stable", "amd64", "", []string{
			"debian-cd/current/amd64/iso-cd/", "debian-cd/current/amd64/iso-dvd/"}},
		{"stable", "arm64", "cd", []string{"debian-cd/current/arm64/iso-cd/"}},
		{"testing", "i386", "dvd", []string{"cdimage/weekly-builds/i386/iso-dvd/"}},
		{"live", "amd64", "", []string{"debian-cd/current-live/amd64/iso-hybrid/"}},
	}

	for _, tc := range cases {
		got, err := directories(tc.channel, tc.arch, tc.media)
		if err != nil {
			t.Fatalf("%s/%s/%s: %v", tc.channel, tc.arch, tc.media, err)
		}
		if strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Errorf("%s/%s/%s = %v, want %v", tc.channel, tc.arch, tc.media, got, tc.want)
		}
	}
	if _, err := directories("nonsense", "amd64", ""); err == nil {
		t.Error("an unknown channel should be rejected")
	}
}

// TestFacetsDependOnTheChannel: live images come in one size only.
func TestFacetsDependOnTheChannel(t *testing.T) {
	p := offline()

	stableList := provider.FacetsAt(p.Facets("stable"), provider.StageList)
	if len(stableList) != 2 || stableList[0].Key != "arch" || stableList[1].Key != "media" {
		t.Errorf("stable list questions = %v, want arch then media", keys(stableList))
	}
	liveList := provider.FacetsAt(p.Facets("live"), provider.StageList)
	if len(liveList) != 1 || liveList[0].Key != "arch" {
		t.Errorf("live list questions = %v, want arch only", keys(liveList))
	}
	if err := provider.Validate(p.Facets("stable"), provider.Selection{"media": "dvd"}); err != nil {
		t.Errorf("media=dvd should be valid for stable: %v", err)
	}
	if err := provider.Validate(p.Facets("live"), provider.Selection{"media": "dvd"}); err == nil {
		t.Error("media should not be offered for live images")
	}
}

// offline returns a provider with its mirror list already filled in, so tests
// never reach for the network.
func offline(sites ...mirrorSite) *Provider {
	p := New()
	p.mirrors.loaded = true
	p.mirrors.sites = sites
	return p
}

func keys(facets []provider.Facet) []string {
	out := make([]string, 0, len(facets))
	for _, f := range facets {
		out = append(out, f.Key)
	}
	return out
}

// TestSourcesFallBackToDebianItself: when the mirror list cannot be measured -
// offline, or a tree that is not mirrored - the download still has somewhere to
// come from.
func TestSourcesFallBackToDebianItself(t *testing.T) {
	const path = "debian-cd/current/amd64/iso-cd/debian-13.7.0-amd64-netinst.iso"
	p := offline()

	from := p.sources(context.Background(), path)
	if from.url != primary+path || len(from.spares) != 0 {
		t.Errorf("with no mirror list: %+v, want Debian's own server alone", from)
	}

	// Weekly builds are not mirrored, so they are never measured.
	const weekly = "cdimage/weekly-builds/amd64/iso-cd/debian-testing-amd64-netinst.iso"
	if from := p.sources(context.Background(), weekly); from.url != primary+weekly || len(from.spares) != 0 {
		t.Errorf("weekly build source = %+v, want Debian's own server", from)
	}
}

// TestNoMirrorMenu: picking a mirror is not a question worth asking, so the
// only questions Debian asks are about the image itself.
func TestNoMirrorMenu(t *testing.T) {
	p := offline(mirrorSite{Host: "mirror.example.net", Path: "/debian-cd/"})
	for _, channel := range []string{"stable", "live", "testing"} {
		for _, f := range p.Facets(channel) {
			if f.Key == "mirror" {
				t.Errorf("%s still asks which mirror to use", channel)
			}
		}
		if got := provider.FacetsAt(p.Facets(channel), provider.StageResolve); len(got) != 0 {
			t.Errorf("%s asks %d questions after choosing a release, want none", channel, len(got))
		}
	}
}

func TestParseChecksumsAndIndex(t *testing.T) {
	sums := webdir.ParseChecksums(strings.NewReader(
		"a7ef94ac2fb9a7fec454552abd629b7cc9d5155c886165a45649f5ce6167e355  debian-13.7.0-amd64-netinst.iso\n" +
			"d33ed0c64bde5ff43445c0c530f9da599954d8585771c55345cb840d7997d4a7  debian-edu-13.7.0-amd64-netinst.iso\n" +
			"malformed line\n"))
	if len(sums) != 2 {
		t.Fatalf("parsed %d checksums, want 2", len(sums))
	}
	if sums["debian-13.7.0-amd64-netinst.iso"] != "a7ef94ac2fb9a7fec454552abd629b7cc9d5155c886165a45649f5ce6167e355" {
		t.Errorf("wrong checksum: %v", sums)
	}

	index := webdir.ParseIndex(`<a href="debian-13.7.0-amd64-netinst.iso">debian-13.7.0-amd64-netinst.iso</a></td>`+
		`<td class="indexcollastmod">2026-09-12 13:34  </td><td class="indexcolsize">756M</td></tr>`+
		`<a href="SHA256SUMS">SHA256SUMS</a></td><td class="indexcollastmod">2026-09-12 13:40  </td><td class="indexcolsize">1.2K</td></tr>`, ".iso")
	if len(index) != 1 {
		t.Fatalf("index kept %d entries, want only the ISO", len(index))
	}
	file := index["debian-13.7.0-amd64-netinst.iso"]
	if file.Size != 756<<20 {
		t.Errorf("size = %d, want %d", file.Size, 756<<20)
	}
	if got := file.Modified.Format("2006-01-02"); got != "2026-09-12" {
		t.Errorf("modified = %s", got)
	}
}

func TestDescribe(t *testing.T) {
	cases := map[string]imageName{
		"debian-13.7.0-amd64-netinst.iso":     {version: "13.7.0", variant: "netinst"},
		"debian-13.7.0-amd64-DVD-1.iso":       {version: "13.7.0", variant: "DVD-1"},
		"debian-edu-13.7.0-amd64-netinst.iso": {version: "13.7.0", variant: "edu netinst"},
		"debian-mac-13.7.0-i386-netinst.iso":  {version: "13.7.0", variant: "mac netinst"},
		"debian-live-13.7.0-amd64-gnome.iso":  {version: "13.7.0", variant: "live gnome"},
	}
	for filename, want := range cases {
		got := describe(filename)
		if got.version != want.version || got.variant != want.variant {
			t.Errorf("describe(%s) = %+v, want %+v", filename, got, want)
		}
	}
}

func TestParseHumanSize(t *testing.T) {
	gib := float64(int64(1) << 30)
	cases := map[string]int64{"756M": 756 << 20, "3.7G": int64(3.7 * gib), "1.2K": 1228, "-": 0, "": 0}
	for in, want := range cases {
		if got := webdir.ParseHumanSize(in); got != want {
			t.Errorf("parseHumanSize(%q) = %d, want %d", in, got, want)
		}
	}
}

// TestPairImagesDropsJigdoOnlyDiscs: Debian publishes a hash for every disc in
// a DVD set, but mirrors carry only the first - listing the rest would offer
// the user downloads that 404.
func TestPairImagesDropsJigdoOnlyDiscs(t *testing.T) {
	sums := webdir.Checksums{
		"debian-13.7.0-arm64-netinst.iso": "a1",
		"debian-13.7.0-arm64-DVD-1.iso":   "b2",
		"debian-13.7.0-arm64-DVD-22.iso":  "c3", // hashed, never served
		"debian-13.7.0-arm64-DVD-1.jigdo": "d4", // not an image at all
	}
	listed := map[string]webdir.File{
		"debian-13.7.0-arm64-netinst.iso": {Name: "debian-13.7.0-arm64-netinst.iso", Size: 701 << 20},
		"debian-13.7.0-arm64-DVD-1.iso":   {Name: "debian-13.7.0-arm64-DVD-1.iso", Size: 3700 << 20},
	}

	images := pairImages(sums, listed, "debian-cd/current/arm64/iso-dvd/", "arm64")
	if len(images) != 2 {
		t.Fatalf("kept %d images, want the two that exist: %v", len(images), images)
	}
	if _, ok := images["debian-13.7.0-arm64-DVD-22.iso"]; ok {
		t.Error("a disc that is not served was offered anyway")
	}
	if img := images["debian-13.7.0-arm64-DVD-1.iso"]; img.file.Size == 0 || img.sha256 != "b2" {
		t.Errorf("DVD-1 lost its size or digest: %+v", img)
	}

	// With no index to cross-check (a mirror that hides listings), fall back to
	// the checksums rather than showing nothing.
	fallback := pairImages(sums, nil, "debian-cd/current/arm64/iso-dvd/", "arm64")
	if len(fallback) != 3 {
		t.Errorf("fallback kept %d images, want all three ISOs", len(fallback))
	}
}

// TestParseMasterlist reads Debian's own mirror list: the menu should offer
// what Debian publishes rather than a few hosts hard-coded here.
func TestParseMasterlist(t *testing.T) {
	sites := parseMasterlist(`Site: cdimage.debian.org
Country: SE Sweden
Type: Origin
CDImage-http: /debian-cd/

Site: debian.inf.tu-dresden.de
Country: DE Germany
Location: Dresden
Archive-http: /debian/
CDImage-http: /debian-cd/
CDImage-rsync: debian-cd/

Site: ftp.example.org
Country: FR France
Archive-http: /debian/

Site: mirror.example.net
Country: NL Netherlands
CDImage-http: /debian-cd
`)

	if len(sites) != 2 {
		t.Fatalf("parsed %d mirrors, want the two that carry CD images: %+v", len(sites), sites)
	}

	dresden := sites[0]
	if dresden.Host != "debian.inf.tu-dresden.de" || dresden.Country != "Germany" || dresden.City != "Dresden" {
		t.Errorf("first mirror = %+v", dresden)
	}
	if got, want := dresden.Label(), "debian.inf.tu-dresden.de - Dresden, Germany"; got != want {
		t.Errorf("label = %q, want %q", got, want)
	}
	// A missing trailing slash must not produce a broken URL.
	if got, _ := sites[1].URL("debian-cd/current/amd64/iso-cd/x.iso"); got != "https://mirror.example.net/debian-cd/current/amd64/iso-cd/x.iso" {
		t.Errorf("url = %q", got)
	}
	// Debian's own server is the default, not a mirror choice.
	for _, site := range sites {
		if site.Host == "cdimage.debian.org" {
			t.Error("the origin should not appear as a mirror")
		}
	}
	// Weekly builds are not part of the mirrored tree.
	if _, ok := dresden.URL("cdimage/weekly-builds/amd64/iso-cd/x.iso"); ok {
		t.Error("mirrors should not be offered for unmirrored trees")
	}
}

func TestSiteByHost(t *testing.T) {
	sites := []mirrorSite{{Host: "a.example"}, {Host: "b.example"}}
	if _, ok := siteByHost(sites, "B.EXAMPLE"); !ok {
		t.Error("host lookup should ignore case")
	}
	if _, ok := siteByHost(sites, "c.example"); ok {
		t.Error("an unknown host should not match")
	}
}
