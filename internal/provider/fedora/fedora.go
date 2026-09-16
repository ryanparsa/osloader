// Package fedora lists and resolves Fedora images. Fedora publishes a single
// JSON document describing every released image, complete with size and
// SHA-256, and download.fedoraproject.org redirects each request to a nearby
// mirror - so the digest, not the host, is what proves the file.
package fedora

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"path"
	"sort"
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

// releasesJSON is Fedora's own index of released images.
const releasesJSON = "https://fedoraproject.org/releases.json"

// mirrorConnections is how many parallel connections one mirror may get: the
// redirector hands us a volunteer mirror, not a CDN.
const mirrorConnections = 4

// release is one entry of releases.json.
type release struct {
	Version    string `json:"version"`
	Arch       string `json:"arch"`
	Link       string `json:"link"`
	Variant    string `json:"variant"`
	Subvariant string `json:"subvariant"`
	SHA256     string `json:"sha256"`
	Size       string `json:"size"`
}

// prerelease reports whether this is a beta or other test build.
func (r release) prerelease() bool {
	return strings.ContainsAny(r.Version, " _") || strings.Contains(strings.ToLower(r.Version), "beta")
}

// bytes parses the size, which Fedora publishes as a string.
func (r release) bytes() int64 {
	size, err := strconv.ParseInt(r.Size, 10, 64)
	if err != nil {
		return 0
	}
	return size
}

// filename is the name the image is saved as.
func (r release) filename() string { return path.Base(r.Link) }

// Provider implements provider.Provider for Fedora.
type Provider struct {
	client *http.Client

	mu       sync.Mutex
	loaded   bool
	releases []release
	logger   *slog.Logger
	agent    string
}

// New returns a Fedora provider.
func New() *Provider {
	return &Provider{
		client: &http.Client{
			Timeout: 2 * time.Minute,
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				DialContext:           (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
				TLSHandshakeTimeout:   15 * time.Second,
				ResponseHeaderTimeout: 30 * time.Second,
				IdleConnTimeout:       90 * time.Second,
				ForceAttemptHTTP2:     true,
			},
		},
		logger: logging.Discard(),
		agent:  ua.Default,
	}
}

var defaultProvider = New()

func init() { provider.Register("fedora", defaultProvider) }

// SetLogger installs the verbose event log.
func SetLogger(l *slog.Logger) {
	if l != nil {
		defaultProvider.logger = l
	}
}

// SetUserAgent overrides the User-Agent used for catalogue requests.
func SetUserAgent(value string) { defaultProvider.agent = ua.Resolve(value) }

// Name implements provider.Provider.
func (p *Provider) Name() string { return "Fedora" }

// Description implements provider.Describer.
func (p *Provider) Description() string {
	return "fedoraproject.org releases.json - SHA-256 and size, mirror chosen by Fedora"
}

// Available implements provider.Provider.
func (p *Provider) Available() bool { return true }

// Channels implements provider.Provider.
func (p *Provider) Channels() []provider.Channel {
	return []provider.Channel{
		{ID: "stable", Label: "Stable releases"},
		{ID: "prerelease", Label: "Beta and test builds"},
	}
}

// catalogue fetches releases.json once and keeps it. A failed fetch is not
// cached, so a menu opened offline can still fill in later.
func (p *Provider) catalogue(ctx context.Context) ([]release, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.loaded {
		return p.releases, nil
	}

	body, err := webdir.FetchText(ctx, p.client, releasesJSON, p.agent, p.logger)
	if err != nil {
		return nil, err
	}
	var all []release
	if err := json.Unmarshal([]byte(body), &all); err != nil {
		return nil, fmt.Errorf("decode %s: %w", releasesJSON, err)
	}

	// Fedora ships raw disk images and container roots in the same document;
	// this tool downloads installable media.
	p.releases = p.releases[:0]
	for _, r := range all {
		if strings.HasSuffix(r.Link, ".iso") && r.SHA256 != "" {
			p.releases = append(p.releases, r)
		}
	}
	p.loaded = true
	p.logger.Info("fedora catalogue", "entries", len(all), "images", len(p.releases))
	return p.releases, nil
}

// known returns whatever catalogue has already been fetched, without going to
// the network - menus are built without a context.
func (p *Provider) menuReleases() []release {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	releases, err := p.catalogue(ctx)
	if err != nil {
		p.logger.Warn("fedora catalogue unavailable", "err", err.Error())
	}
	return releases
}

// Facets implements provider.Provider: the values come from the catalogue, so
// the menu offers exactly what Fedora built for this release.
func (p *Provider) Facets(channel string) []provider.Facet {
	releases := p.menuReleases()

	arches, variants := map[string]struct{}{}, map[string]struct{}{}
	for _, r := range releases {
		if inChannel(r, channel) {
			arches[r.Arch] = struct{}{}
			variants[r.Variant] = struct{}{}
		}
	}

	return []provider.Facet{
		{Key: "arch", Label: "Architecture", AllowAll: true, FreeForm: true, Values: values(arches, archLabels)},
		{Key: "variant", Label: "Edition", AllowAll: true, FreeForm: true, Values: values(variants, nil)},
	}
}

// archLabels spells out what Fedora's architecture names mean.
var archLabels = map[string]string{
	"x86_64":  "x86_64 - 64-bit PC",
	"aarch64": "aarch64 - 64-bit ARM",
	"ppc64le": "ppc64le - POWER",
	"s390x":   "s390x - IBM Z",
}

// values turns a set into sorted facet values.
func values(set map[string]struct{}, labels map[string]string) []provider.FacetValue {
	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	out := make([]provider.FacetValue, 0, len(ids))
	for _, id := range ids {
		label := id
		if labels != nil {
			if better, ok := labels[id]; ok {
				label = better
			}
		}
		out = append(out, provider.FacetValue{ID: id, Label: label})
	}
	return out
}

// inChannel reports whether a release belongs to the requested channel.
func inChannel(r release, channel string) bool {
	if channel == "prerelease" {
		return r.prerelease()
	}
	return !r.prerelease()
}

// List implements provider.Provider.
func (p *Provider) List(ctx context.Context, channel string, sel provider.Selection) ([]provider.Release, error) {
	if err := provider.Validate(p.Facets(channel), sel); err != nil {
		return nil, err
	}
	catalogue, err := p.catalogue(ctx)
	if err != nil {
		return nil, err
	}

	var releases []provider.Release
	for _, r := range catalogue {
		if !inChannel(r, channel) || !matches(r, sel) {
			continue
		}
		title := "Fedora " + r.Variant
		if r.Subvariant != "" && r.Subvariant != r.Variant {
			title += " " + r.Subvariant
		}
		releases = append(releases, provider.Release{
			ID:      r.filename(),
			OS:      "fedora",
			Channel: channel,
			Version: r.Version,
			Arch:    r.Arch,
			Title:   title + " · " + r.Arch,
			Size:    r.bytes(),
		})
	}
	if len(releases) == 0 {
		return nil, fmt.Errorf("no Fedora images matched")
	}

	// Fedora publishes no dates here, so order by version and then by name.
	provider.Sort(releases, provider.SortVersion, false)
	return releases, nil
}

// matches applies the selection to one release.
func matches(r release, sel provider.Selection) bool {
	if arch := sel.Get("arch"); arch != "" && r.Arch != arch {
		return false
	}
	if variant := sel.Get("variant"); variant != "" && !strings.EqualFold(r.Variant, variant) {
		return false
	}
	return true
}

// Resolve implements provider.Provider. The link goes through Fedora's
// redirector, which sends the request to a nearby mirror; the published SHA-256
// is what makes that safe.
func (p *Provider) Resolve(ctx context.Context, r provider.Release, _ provider.Selection) (provider.Artifact, error) {
	catalogue, err := p.catalogue(ctx)
	if err != nil {
		return provider.Artifact{}, err
	}

	for _, entry := range catalogue {
		if entry.filename() != r.ID || !inChannel(entry, r.Channel) {
			continue
		}
		return provider.Artifact{
			URL:        entry.Link,
			Filename:   entry.filename(),
			Size:       entry.bytes(),
			MaxPerHost: mirrorConnections,
			Digest:     entry.SHA256,
			Verifier:   verify.Checksum("sha256", entry.SHA256, "Fedora's releases.json", entry.bytes()),
		}, nil
	}
	return provider.Artifact{}, fmt.Errorf("image %s not found in the %s channel", r.ID, r.Channel)
}
