package macos

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/ryanparsa/osloader/internal/provider"
	"github.com/ryanparsa/osloader/internal/verify"
)

// allowedHosts pins downloads to Apple. A tampered catalog or a hostile proxy
// must not be able to point us at some other server.
var allowedHosts = []string{"swcdn.apple.com", ".apple.com"}

// distWorkers bounds the concurrent .dist fetches.
const distWorkers = 8

// Provider implements provider.Provider against Apple's Software Update catalog.
type Provider struct {
	client     *http.Client
	mu         sync.Mutex
	catalogURL string                        // optional override for every channel
	cache      map[string]map[string]product // channel -> products
}

// New returns a macOS provider with a client tuned for many small requests.
func New() *Provider {
	return &Provider{
		client: &http.Client{
			Timeout: 2 * time.Minute,
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				DialContext:           (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
				TLSHandshakeTimeout:   15 * time.Second,
				ResponseHeaderTimeout: 30 * time.Second,
				MaxIdleConnsPerHost:   distWorkers,
				IdleConnTimeout:       90 * time.Second,
				ForceAttemptHTTP2:     true,
			},
		},
		cache: map[string]map[string]product{},
	}
}

var defaultProvider = New()

func init() { provider.Register("macos", defaultProvider) }

// SetCatalogURL overrides the catalog for every channel (the --catalog-url flag).
func SetCatalogURL(u string) { defaultProvider.catalogURL = u }

// Default returns the registered provider, for callers that need macOS-only extras.
func Default() *Provider { return defaultProvider }

// Name implements provider.Provider.
func (p *Provider) Name() string { return "macOS" }

// Available implements provider.Provider.
func (p *Provider) Available() bool { return true }

// Channels implements provider.Provider.
func (p *Provider) Channels() []provider.Channel {
	return []provider.Channel{
		{ID: "public", Label: "Public - released builds"},
		{ID: "devseed", Label: "Developer seed - developer betas"},
		{ID: "beta", Label: "Public beta"},
		{ID: "customerseed", Label: "Customer seed - AppleSeed"},
	}
}

// products fetches (and caches) the full installers for a channel.
func (p *Provider) products(ctx context.Context, channel string) (map[string]product, error) {
	p.mu.Lock()
	cached, ok := p.cache[channel]
	url := p.catalogURL
	p.mu.Unlock()
	if ok {
		return cached, nil
	}

	if url == "" {
		var err error
		if url, err = CatalogURL(channel); err != nil {
			return nil, err
		}
	}
	found, err := fetchCatalog(ctx, p.client, url)
	if err != nil {
		return nil, err
	}

	p.mu.Lock()
	p.cache[channel] = found
	p.mu.Unlock()
	return found, nil
}

// Facets implements provider.Provider: Apple ships one installer per release,
// so there is nothing further to ask.
func (p *Provider) Facets(string) []provider.Facet { return nil }

// List implements provider.Provider: catalog, then .dist files for the titles.
func (p *Provider) List(ctx context.Context, channel string, _ provider.Selection) ([]provider.Release, error) {
	products, err := p.products(ctx, channel)
	if err != nil {
		return nil, err
	}
	dists := fetchDists(ctx, p.client, products, distWorkers)

	releases := make([]provider.Release, 0, len(products))
	for id, prod := range products {
		installer, ok := prod.installer()
		if !ok {
			continue
		}
		info := dists[id]
		releases = append(releases, provider.Release{
			ID:      id,
			OS:      "macos",
			Channel: channel,
			Version: info.Version,
			Build:   info.Build,
			Title:   info.Title,
			Posted:  prod.PostDate,
			Size:    installer.Size,
		})
	}
	provider.SortReleases(releases)
	return releases, nil
}

// Resolve implements provider.Provider, returning the InstallAssistant package.
// Apple serves one CDN, so there is nothing to choose here.
func (p *Provider) Resolve(ctx context.Context, r provider.Release, _ provider.Selection) (provider.Artifact, error) {
	return p.ResolveNamed(ctx, r, "")
}

// ResolveNamed returns a specific package from the product by file name - the
// installer itself when name is empty. Naming a small companion file (e.g.
// InstallInfo.plist) makes an end-to-end smoke test cheap.
func (p *Provider) ResolveNamed(ctx context.Context, r provider.Release, name string) (provider.Artifact, error) {
	products, err := p.products(ctx, r.Channel)
	if err != nil {
		return provider.Artifact{}, err
	}
	prod, ok := products[r.ID]
	if !ok {
		return provider.Artifact{}, fmt.Errorf("product %s not found in the %s catalog", r.ID, r.Channel)
	}

	chosen, ok := prod.installer()
	if name != "" {
		chosen, ok = pkg{}, false
		for _, candidate := range prod.Packages {
			if path.Base(candidate.URL) == name {
				chosen, ok = candidate, true
				break
			}
		}
	}
	if !ok {
		return provider.Artifact{}, fmt.Errorf("product %s has no package named %q", r.ID, name)
	}

	return provider.Artifact{
		URL:          chosen.URL,
		Filename:     artifactName(r, path.Base(chosen.URL)),
		Size:         chosen.Size,
		AllowedHosts: allowedHosts,
		Digest:       chosen.Digest,
		Verifier:     verify.ApplePkg(chosen.Size),
	}, nil
}

// artifactName labels the file with what it actually is, so a Downloads folder
// with several installers in it stays readable.
func artifactName(r provider.Release, base string) string {
	version, build := sanitize(r.Version), sanitize(r.Build)
	if version == "" && build == "" {
		return base
	}
	prefix := strings.Trim("macOS_"+version+"_"+build, "_")
	return prefix + "_" + base
}

// sanitize keeps a version or build safe to embed in a file name.
func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-':
			return r
		default:
			return -1
		}
	}, strings.TrimSpace(s))
}
