package debian

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ryanparsa/osloader/internal/logging"
	"github.com/ryanparsa/osloader/internal/provider"
	"github.com/ryanparsa/osloader/internal/provider/webdir"
	"github.com/ryanparsa/osloader/internal/ua"
	"github.com/ryanparsa/osloader/internal/verify"
)

// primary is where the catalogue (SHA256SUMS and the directory index) is read
// from: an HTTPS origin run by Debian itself. It redirects downloads to a
// mirror, which is fine - the digest published here is what a download is
// checked against.
const primary = "https://cdimage.debian.org/"

// mirrorConnections is how many parallel connections one mirror may get.
const mirrorConnections = 4

// architectures kept in the listing. Debian publishes more; these are the ones
// people actually install from.
var architectures = []string{"amd64", "arm64", "i386"}

// indexWorkers bounds the concurrent directory fetches.
const indexWorkers = 8

// Provider implements provider.Provider for Debian images.
type Provider struct {
	client *http.Client

	mu      sync.Mutex
	cache   map[string]map[string]image // channel -> file name -> image
	mirrors mirrorCache
	logger  *slog.Logger
	agent   string
}

// image is one downloadable ISO.
type image struct {
	file     webdir.File
	path     string // path under the mirror root
	sha256   string
	arch     string
	version  string
	variant  string
	modified time.Time
}

// New returns a Debian provider.
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

func init() { provider.Register("debian", defaultProvider) }

// SetLogger installs the verbose event log.
func SetLogger(l *slog.Logger) {
	if l != nil {
		defaultProvider.logger = l
	}
}

// SetUserAgent overrides the User-Agent used for catalogue requests.
func SetUserAgent(value string) { defaultProvider.agent = ua.Resolve(value) }

// Name implements provider.Provider.
func (p *Provider) Name() string { return "Debian" }

// Description implements provider.Describer.
func (p *Provider) Description() string {
	return "cdimage.debian.org - SHA-256 from the signed SHA256SUMS, fastest mirrors measured"
}

// Available implements provider.Provider.
func (p *Provider) Available() bool { return true }

// Channels implements provider.Provider.
func (p *Provider) Channels() []provider.Channel {
	return []provider.Channel{
		{ID: "stable", Label: "Stable - installer images"},
		{ID: "live", Label: "Stable - live images"},
		{ID: "testing", Label: "Testing - weekly builds"},
	}
}

// Facets implements provider.Provider. Debian builds every image for several
// architectures and in CD and DVD sizes, so those are the questions worth
// asking before showing a list.
func (p *Provider) Facets(channel string) []provider.Facet {
	arch := provider.Facet{
		Key: "arch", Label: "Architecture", AllowAll: true,
		Values: []provider.FacetValue{
			{ID: "amd64", Label: "amd64 - 64-bit PC"},
			{ID: "arm64", Label: "arm64 - 64-bit ARM"},
			{ID: "i386", Label: "i386 - 32-bit PC"},
		},
	}
	if channel == "live" {
		return []provider.Facet{arch}
	}
	return []provider.Facet{arch, {
		Key: "media", Label: "Image size", AllowAll: true,
		Values: []provider.FacetValue{
			{ID: "cd", Label: "CD - netinst and small images"},
			{ID: "dvd", Label: "DVD - full first disc"},
		},
	}}
}

// sources picks where a download comes from. Debian has around ninety mirrors
// and no way to know which is close to this machine, so rather than asking the
// user to guess, the mirrors are measured against the file itself and the
// quickest ten are used together: connections spread across them, and any that
// fails is dropped mid-download without the transfer noticing.
func (p *Provider) sources(ctx context.Context, path string) source {
	official := primary + path

	// Weekly builds are only on Debian's own server.
	if !strings.HasPrefix(path, "debian-cd/") {
		return source{url: official}
	}

	ranked, err := p.rankMirrors(ctx, path)
	if err != nil || len(ranked) == 0 {
		if err != nil {
			p.logger.Warn("using Debian's own server", "err", err.Error())
		}
		return source{url: official}
	}

	chosen := source{url: ranked[0].url}
	for _, m := range ranked[1:] {
		chosen.spares = append(chosen.spares, m.url)
	}
	// Debian's own server stands behind the mirrors: it redirects to one of
	// them anyway, and it is the one host guaranteed to have the file.
	chosen.spares = append(chosen.spares, official)

	p.logger.Info("sources chosen", "fastest", ranked[0].site.Host, "mirrors", len(ranked))
	return chosen
}

// source is where a download should come from: the quickest address, and the
// others to spread across and fall back to.
type source struct {
	url    string
	spares []string
}

// mediaDirs maps the media facet to the directories Debian publishes.
func mediaDirs(media string) []string {
	switch media {
	case "cd":
		return []string{"iso-cd"}
	case "dvd":
		return []string{"iso-dvd"}
	default:
		return []string{"iso-cd", "iso-dvd"}
	}
}

// directories returns the paths to scan for a channel, honouring the selection
// so choosing one architecture also means fetching a fraction of the indexes.
func directories(channel, arch, media string) ([]string, error) {
	var root string
	switch channel {
	case "stable":
		root = "debian-cd/current/"
	case "live":
		return []string{"debian-cd/current-live/" + arch + "/iso-hybrid/"}, nil
	case "testing":
		root = "cdimage/weekly-builds/"
	default:
		return nil, fmt.Errorf("unknown Debian channel %q", channel)
	}

	dirs := make([]string, 0, 2)
	for _, sub := range mediaDirs(media) {
		dirs = append(dirs, root+arch+"/"+sub+"/")
	}
	return dirs, nil
}

// List implements provider.Provider: read every directory for the channel,
// pairing each ISO with its published digest.
func (p *Provider) List(ctx context.Context, channel string, sel provider.Selection) ([]provider.Release, error) {
	if err := provider.Validate(p.Facets(channel), sel); err != nil {
		return nil, err
	}
	images, err := p.images(ctx, channel, sel)
	if err != nil {
		return nil, err
	}
	if channel != "testing" {
		// Warm the mirror list while the user is reading the release list, so
		// the Source menu opens instantly.
		go p.sites(context.WithoutCancel(ctx))
	}

	releases := make([]provider.Release, 0, len(images))
	for name, img := range images {
		releases = append(releases, provider.Release{
			ID:      name,
			OS:      "debian",
			Channel: channel,
			Version: img.version,
			Arch:    img.arch,
			Title:   "Debian " + img.variant + " · " + img.arch,
			Posted:  img.modified,
			Size:    img.file.Size,
		})
	}
	if len(releases) == 0 {
		return nil, fmt.Errorf("no Debian images found in the %s channel", channel)
	}
	provider.SortReleases(releases)
	return releases, nil
}

// images fetches (and caches) the images for a channel.
func (p *Provider) images(ctx context.Context, channel string, sel provider.Selection) (map[string]image, error) {
	key := channel + "|" + sel.String()

	p.mu.Lock()
	cached, ok := p.cache[key]
	p.mu.Unlock()
	if ok {
		return cached, nil
	}

	wanted := architectures
	if arch := sel.Get("arch"); arch != "" {
		wanted = []string{arch}
	}

	var paths []string
	for _, arch := range wanted {
		dirs, err := directories(channel, arch, sel.Get("media"))
		if err != nil {
			return nil, err
		}
		paths = append(paths, dirs...)
	}

	found := p.scan(ctx, paths)
	if len(found) == 0 {
		return nil, fmt.Errorf("no Debian images found in the %s channel", channel)
	}

	p.mu.Lock()
	p.cache[key] = found
	p.mu.Unlock()
	return found, nil
}

// scan reads the directories concurrently. A directory that does not exist for
// an architecture (no DVD images, say) is skipped rather than fatal.
func (p *Provider) scan(ctx context.Context, paths []string) map[string]image {
	jobs := make(chan string)
	out := map[string]image{}
	var mu sync.Mutex
	var wg sync.WaitGroup

	for i := 0; i < indexWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for path := range jobs {
				images, err := p.scanDirectory(ctx, path)
				if err != nil {
					p.logger.Debug("skipping directory", "path", path, "err", err.Error())
					continue
				}
				mu.Lock()
				for name, img := range images {
					out[name] = img
				}
				mu.Unlock()
			}
		}()
	}

	for _, path := range paths {
		select {
		case jobs <- path:
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

// scanDirectory pairs the digests in SHA256SUMS with the sizes and dates in the
// directory index.
func (p *Provider) scanDirectory(ctx context.Context, path string) (map[string]image, error) {
	sumsBody, err := webdir.FetchText(ctx, p.client, primary+path+"SHA256SUMS", p.agent, p.logger)
	if err != nil {
		return nil, err
	}
	sums := webdir.ParseChecksums(strings.NewReader(sumsBody))

	indexBody, err := webdir.FetchText(ctx, p.client, primary+path, p.agent, p.logger)
	if err != nil {
		p.logger.Debug("no directory index", "path", path, "err", err.Error())
	}
	listed := webdir.ParseIndex(indexBody, ".iso")

	images := pairImages(sums, listed, path, architectureFromPath(path))
	p.logger.Info("debian directory", "path", path,
		"checksums", len(sums), "listed", len(listed), "images", len(images))
	return images, nil
}

// pairImages keeps only images that are actually downloadable. SHA256SUMS also
// covers images that exist solely as jigdo recipes - a Debian DVD set lists 27
// hashes while the mirror carries only the first disc - and offering those
// would mean handing the user a 404.
func pairImages(sums webdir.Checksums, listed map[string]webdir.File, path, arch string) map[string]image {
	images := make(map[string]image, len(sums))
	for name, sum := range sums {
		if !strings.HasSuffix(name, ".iso") {
			continue
		}
		file, present := listed[name]
		if len(listed) > 0 && !present {
			continue // hashed for jigdo, but not served here
		}
		file.Name = name

		described := describe(name)
		images[name] = image{
			file:     file,
			path:     path + name,
			sha256:   sum,
			arch:     arch,
			version:  described.version,
			variant:  described.variant,
			modified: file.Modified,
		}
	}
	return images
}

// architectureFromPath reads the architecture out of a cdimage path.
func architectureFromPath(path string) string {
	for _, arch := range architectures {
		if strings.Contains(path, "/"+arch+"/") {
			return arch
		}
	}
	return "unknown"
}

// Resolve implements provider.Provider. The download may come from any mirror,
// so the digest - read from Debian's own HTTPS origin - is the gate, and the
// host is deliberately not pinned.
func (p *Provider) Resolve(ctx context.Context, r provider.Release, sel provider.Selection) (provider.Artifact, error) {
	// Resolve by architecture rather than the whole channel: one directory is
	// enough to find the image again, whatever the user filtered by earlier.
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

	from := p.sources(ctx, img.path)

	return provider.Artifact{
		URL:      from.url,
		Mirrors:  from.spares,
		Filename: img.file.Name,
		// Volunteer mirrors are not CDNs: a handful of connections each is the
		// polite maximum, and several of them answer 403 to more.
		MaxPerHost: mirrorConnections,
		Digest:     img.sha256,
		Verifier:   verify.Checksum("sha256", img.sha256, "Debian's signed SHA256SUMS", 0),
	}, nil
}
