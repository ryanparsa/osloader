// Package ubuntu lists and resolves Ubuntu images. Canonical publishes amd64
// images on releases.ubuntu.com and every other architecture on
// cdimage.ubuntu.com, each directory carrying a SHA256SUMS (signed alongside as
// SHA256SUMS.gpg) that a download can be checked against.
package ubuntu

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ryanparsa/osloader/internal/logging"
	"github.com/ryanparsa/osloader/internal/provider"
	"github.com/ryanparsa/osloader/internal/provider/webdir"
	"github.com/ryanparsa/osloader/internal/ua"
	"github.com/ryanparsa/osloader/internal/verify"
)

const (
	// releasesRoot carries the amd64 images.
	releasesRoot = "https://releases.ubuntu.com/"
	// cdimageRoot carries every other architecture.
	cdimageRoot = "https://cdimage.ubuntu.com/releases/"

	indexWorkers = 8
)

// architectures Canonical builds installer images for.
var architectures = []provider.FacetValue{
	{ID: "amd64", Label: "amd64 - 64-bit PC"},
	{ID: "arm64", Label: "arm64 - 64-bit ARM"},
	{ID: "ppc64el", Label: "ppc64el - POWER"},
	{ID: "riscv64", Label: "riscv64 - RISC-V"},
	{ID: "s390x", Label: "s390x - IBM Z"},
}

// Provider implements provider.Provider for Ubuntu.
type Provider struct {
	client *http.Client

	mu     sync.Mutex
	cache  map[string]map[string]image
	logger *slog.Logger
	agent  string
}

// image is one downloadable ISO.
type image struct {
	file    webdir.File
	url     string
	sha256  string
	arch    string
	version string
	variant string
}

// New returns an Ubuntu provider.
func New() *Provider {
	return &Provider{
		client: &http.Client{
			Timeout: 2 * time.Minute,
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				DialContext:           (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
				TLSHandshakeTimeout:   15 * time.Second,
				ResponseHeaderTimeout: 30 * time.Second,
				MaxIdleConnsPerHost:   indexWorkers,
				IdleConnTimeout:       90 * time.Second,
				ForceAttemptHTTP2:     true,
			},
		},
		cache:  map[string]map[string]image{},
		logger: logging.Discard(),
		agent:  ua.Default,
	}
}

var defaultProvider = New()

func init() { provider.Register("ubuntu", defaultProvider) }

// SetLogger installs the verbose event log.
func SetLogger(l *slog.Logger) {
	if l != nil {
		defaultProvider.logger = l
	}
}

// SetUserAgent overrides the User-Agent used for catalogue requests.
func SetUserAgent(value string) { defaultProvider.agent = ua.Resolve(value) }

// Name implements provider.Provider.
func (p *Provider) Name() string { return "Ubuntu" }

// Description implements provider.Describer.
func (p *Provider) Description() string {
	return "releases.ubuntu.com - SHA-256 from the signed SHA256SUMS"
}

// Available implements provider.Provider.
func (p *Provider) Available() bool { return true }

// Channels implements provider.Provider. The split that matters to someone
// installing Ubuntu is how long the release is supported.
func (p *Provider) Channels() []provider.Channel {
	return []provider.Channel{
		{ID: "lts", Label: "LTS - supported for years"},
		{ID: "all", Label: "All supported releases"},
	}
}

// Facets implements provider.Provider.
func (p *Provider) Facets(string) []provider.Facet {
	return []provider.Facet{
		{Key: "arch", Label: "Architecture", AllowAll: true, Values: architectures},
		{Key: "variant", Label: "Edition", AllowAll: true, Values: []provider.FacetValue{
			{ID: "desktop", Label: "Desktop - graphical installer"},
			{ID: "server", Label: "Server - live server installer"},
		}},
	}
}

// List implements provider.Provider.
func (p *Provider) List(ctx context.Context, channel string, sel provider.Selection) ([]provider.Release, error) {
	if err := provider.Validate(p.Facets(channel), sel); err != nil {
		return nil, err
	}
	images, err := p.images(ctx, channel, sel)
	if err != nil {
		return nil, err
	}

	releases := make([]provider.Release, 0, len(images))
	for name, img := range images {
		releases = append(releases, provider.Release{
			ID:      name,
			OS:      "ubuntu",
			Channel: channel,
			Version: img.version,
			Arch:    img.arch,
			Title:   "Ubuntu " + img.variant + " · " + img.arch,
			Posted:  img.file.Modified,
			Size:    img.file.Size,
		})
	}
	if len(releases) == 0 {
		return nil, fmt.Errorf("no Ubuntu images matched")
	}
	provider.SortReleases(releases)
	return releases, nil
}

// images fetches (and caches) the images for a channel and selection.
func (p *Provider) images(ctx context.Context, channel string, sel provider.Selection) (map[string]image, error) {
	key := channel + "|" + sel.String()

	p.mu.Lock()
	cached, ok := p.cache[key]
	p.mu.Unlock()
	if ok {
		return cached, nil
	}

	versions, err := p.versions(ctx, channel)
	if err != nil {
		return nil, err
	}

	found := p.scan(ctx, versions, sel)
	if len(found) == 0 {
		return nil, fmt.Errorf("no Ubuntu images found for %s", channel)
	}

	p.mu.Lock()
	p.cache[key] = found
	p.mu.Unlock()
	return found, nil
}

// versionDir matches the release directories on releases.ubuntu.com.
var versionDir = regexp.MustCompile(`^(\d+\.\d+(?:\.\d+)?)/$`)

// versions lists the releases Canonical currently publishes, filtered to the
// long-term ones when that is the channel.
func (p *Provider) versions(ctx context.Context, channel string) ([]string, error) {
	body, err := webdir.FetchText(ctx, p.client, releasesRoot, p.agent, p.logger)
	if err != nil {
		return nil, err
	}

	var versions []string
	seen := map[string]struct{}{}
	for _, link := range webdir.Links(body, "/") {
		match := versionDir.FindStringSubmatch(link)
		if match == nil {
			continue
		}
		version := match[1]
		if channel == "lts" && !isLTS(version) {
			continue
		}
		if _, dup := seen[version]; dup {
			continue
		}
		seen[version] = struct{}{}
		versions = append(versions, version)
	}
	if len(versions) == 0 {
		return nil, fmt.Errorf("no Ubuntu releases listed at %s", releasesRoot)
	}
	p.logger.Info("ubuntu releases", "channel", channel, "versions", len(versions))
	return versions, nil
}

// isLTS reports whether a version is a long-term support release: Canonical
// ships those every second April, so an even year with an .04 suffix.
func isLTS(version string) bool {
	parts := strings.Split(version, ".")
	if len(parts) < 2 || parts[1] != "04" {
		return false
	}
	year, err := strconv.Atoi(parts[0])
	return err == nil && year%2 == 0
}

// directories returns the URLs to read for a version, skipping hosts the
// selection rules out: amd64 lives on releases.ubuntu.com and everything else
// on cdimage.ubuntu.com, so choosing an architecture halves the requests.
func directories(version, arch string) []string {
	var dirs []string
	if arch == "" || arch == "amd64" {
		dirs = append(dirs, releasesRoot+version+"/")
	}
	if arch != "amd64" {
		dirs = append(dirs, cdimageRoot+version+"/release/")
	}
	return dirs
}

// scan reads the directories concurrently, skipping any that do not exist for
// a given release.
func (p *Provider) scan(ctx context.Context, versions []string, sel provider.Selection) map[string]image {
	var dirs []string
	for _, version := range versions {
		dirs = append(dirs, directories(version, sel.Get("arch"))...)
	}

	jobs := make(chan string)
	out := map[string]image{}
	var mu sync.Mutex
	var wg sync.WaitGroup

	for i := 0; i < indexWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for dir := range jobs {
				images, err := p.scanDirectory(ctx, dir)
				if err != nil {
					p.logger.Debug("skipping directory", "url", dir, "err", err.Error())
					continue
				}
				mu.Lock()
				for name, img := range images {
					if matches(img, sel) {
						out[name] = img
					}
				}
				mu.Unlock()
			}
		}()
	}

	for _, dir := range dirs {
		select {
		case jobs <- dir:
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return out
		}
	}
	close(jobs)
	wg.Wait()
	return out
}

// matches applies the selection to one image.
func matches(img image, sel provider.Selection) bool {
	if arch := sel.Get("arch"); arch != "" && img.arch != arch {
		return false
	}
	if variant := sel.Get("variant"); variant != "" && img.variant != variant {
		return false
	}
	return true
}

// scanDirectory pairs the digests in SHA256SUMS with the directory listing.
func (p *Provider) scanDirectory(ctx context.Context, dir string) (map[string]image, error) {
	sumsBody, err := webdir.FetchText(ctx, p.client, dir+"SHA256SUMS", p.agent, p.logger)
	if err != nil {
		return nil, err
	}
	sums := webdir.ParseChecksums(strings.NewReader(sumsBody))

	indexBody, err := webdir.FetchText(ctx, p.client, dir, p.agent, p.logger)
	if err != nil {
		p.logger.Debug("no directory index", "url", dir, "err", err.Error())
	}
	listed := webdir.ParseIndex(indexBody, ".iso")

	images := pairImages(sums, listed, dir)
	p.logger.Info("ubuntu directory", "url", dir, "images", len(images))
	return images, nil
}

// pairImages keeps the ISOs, pairing each with its digest and listed size.
func pairImages(sums webdir.Checksums, listed map[string]webdir.File, dir string) map[string]image {
	images := make(map[string]image, len(sums))
	for name, sum := range sums {
		described, ok := describe(name)
		if !ok {
			continue
		}
		file := listed[name]
		file.Name = name

		images[name] = image{
			file:    file,
			url:     dir + name,
			sha256:  sum,
			arch:    described.arch,
			version: described.version,
			variant: described.variant,
		}
	}
	return images
}

// imageName is what an Ubuntu file name says about the image.
type imageName struct {
	version string
	variant string
	arch    string
}

// isoName matches "ubuntu-24.04.5.1-desktop-amd64.iso" and its relatives,
// including the "live-server" naming and suffixed builds such as
// "arm64+largemem".
var isoName = regexp.MustCompile(`^ubuntu-(\d[\d.]*)-(.+)-(amd64|arm64|ppc64el|riscv64|s390x)(\+[\w.]+)?\.iso$`)

func describe(filename string) (imageName, bool) {
	match := isoName.FindStringSubmatch(filename)
	if match == nil {
		return imageName{}, false
	}

	name := imageName{version: match[1], variant: match[2], arch: match[3] + match[4]}
	// "live-server" is how the file is named and "server" is what people call
	// it; the facet uses the shorter one.
	name.variant = strings.TrimPrefix(name.variant, "live-")
	return name, true
}

// Resolve implements provider.Provider. Canonical serves these itself, so there
// is no mirror to choose; the digest from SHA256SUMS is the gate.
func (p *Provider) Resolve(ctx context.Context, r provider.Release, _ provider.Selection) (provider.Artifact, error) {
	lookup := provider.Selection{}
	if r.Arch != "" {
		lookup = lookup.With("arch", r.Arch)
	}
	images, err := p.images(ctx, r.Channel, lookup)
	if err != nil {
		return provider.Artifact{}, err
	}
	img, ok := images[r.ID]
	if !ok {
		return provider.Artifact{}, fmt.Errorf("image %s not found in the %s channel", r.ID, r.Channel)
	}

	return provider.Artifact{
		URL:      img.url,
		Filename: img.file.Name,
		Digest:   img.sha256,
		Verifier: verify.Checksum("sha256", img.sha256, "Ubuntu's signed SHA256SUMS", 0),
	}, nil
}
