// Package macos reads Apple's Software Update catalog and turns it into
// downloadable macOS full installers. Apple documents none of this, so the wire
// formats and quirks handled here were verified against the live servers and are
// explained where they are used.
package macos

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"howett.net/plist"

	"github.com/ryanparsa/osloader/internal/logging"
	"github.com/ryanparsa/osloader/internal/ua"
)

// userAgent is what we tell Apple's servers we are; SetUserAgent changes it.
var userAgent = ua.Default

// logger records what the provider fetched, for --verbose.
var logger = logging.Discard()

// SetLogger installs the verbose event log.
func SetLogger(l *slog.Logger) {
	if l != nil {
		logger = l
	}
}

// SetUserAgent overrides the User-Agent used for catalog and dist requests.
func SetUserAgent(value string) { userAgent = ua.Resolve(value) }

const (
	catalogBase = "https://swscan.apple.com/content/catalogs/others/"

	// majorVersion leads the catalog filename; bump it when the next macOS
	// ships (the older catalogs keep working, they just stop gaining builds).
	majorVersion = "27"

	// versionList is the rest of the filename: every OS generation the catalog
	// covers, newest first.
	versionList = "27-26-15-14-13-12-10.16-10.15-10.14-10.13-10.12-10.11-10.10-10.9-" +
		"mountainlion-lion-snowleopard-leopard"

	// installAssistant is the package that is the full installer itself.
	installAssistant = "/InstallAssistant.pkg"
)

// channelTokens maps a channel ID to the token Apple inserts after the leading
// major version in the catalog filename. Public has no token at all.
var channelTokens = map[string]string{
	"public":       "",
	"devseed":      "seed",
	"beta":         "beta",
	"customerseed": "customerseed",
}

// CatalogURL builds the sucatalog URL for a channel.
func CatalogURL(channel string) (string, error) {
	token, ok := channelTokens[channel]
	if !ok {
		return "", fmt.Errorf("unknown macOS channel %q", channel)
	}
	// Public is just the version list; the others prepend "<major><token>-",
	// e.g. index-27seed-27-26-15-….
	name := versionList
	if token != "" {
		name = majorVersion + token + "-" + versionList
	}
	return catalogBase + "index-" + name + ".merged-1.sucatalog.gz", nil
}

// pkg is one downloadable file inside a product.
type pkg struct {
	URL              string `plist:"URL"`
	Size             int64  `plist:"Size"`
	Digest           string `plist:"Digest"`
	MetadataURL      string `plist:"MetadataURL"`
	IntegrityDataURL string `plist:"IntegrityDataURL"`
}

// extendedMetaInfo carries the marker that distinguishes a full installer.
type extendedMetaInfo struct {
	InstallAssistantPackageIdentifiers map[string]string `plist:"InstallAssistantPackageIdentifiers"`
}

// product is one entry in the catalog's Products dictionary.
type product struct {
	PostDate         time.Time         `plist:"PostDate"`
	Packages         []pkg             `plist:"Packages"`
	ExtendedMetaInfo extendedMetaInfo  `plist:"ExtendedMetaInfo"`
	Distributions    map[string]string `plist:"Distributions"`
}

// installer is the package within a product that is the installer itself.
func (p product) installer() (pkg, bool) {
	for _, pk := range p.Packages {
		if strings.HasSuffix(pk.URL, installAssistant) {
			return pk, true
		}
	}
	return pkg{}, false
}

// isFullInstaller applies the two-part test for a modern full installer: the
// product must advertise InstallAssistant package identifiers and actually ship
// the pkg. That deliberately excludes Catalina and earlier, which used a
// different package layout.
func (p product) isFullInstaller() bool {
	if len(p.ExtendedMetaInfo.InstallAssistantPackageIdentifiers) == 0 {
		return false
	}
	_, ok := p.installer()
	return ok
}

// distURL picks the English distribution descriptor, falling back to any
// language present so an unusual product still yields a title.
func (p product) distURL() string {
	if u, ok := p.Distributions["English"]; ok {
		return u
	}
	for _, u := range p.Distributions {
		return u
	}
	return ""
}

type catalog struct {
	Products map[string]product `plist:"Products"`
}

// fetchCatalog downloads and decodes a sucatalog, keeping only full installers.
func fetchCatalog(ctx context.Context, client *http.Client, url string) (map[string]product, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("catalog %s: unexpected status %s", url, resp.Status)
	}

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	installers, err := decodeCatalog(raw)
	if err != nil {
		return nil, fmt.Errorf("catalog %s: %w", url, err)
	}
	logger.Info("catalog", "url", url, "bytes", len(raw), "installers", len(installers))
	return installers, nil
}

// decodeCatalog gunzips if needed, parses the plist, and keeps only the
// products that are genuinely full installers.
func decodeCatalog(raw []byte) (map[string]product, error) {
	raw, err := maybeGunzip(raw)
	if err != nil {
		return nil, err
	}

	var c catalog
	if _, err := plist.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}

	installers := make(map[string]product, 64)
	for id, p := range c.Products {
		if p.isFullInstaller() {
			installers[id] = p
		}
	}
	if len(installers) == 0 {
		return nil, errors.New("contained no full installers")
	}
	return installers, nil
}

// maybeGunzip decompresses only if the body really is gzip: Apple serves the
// .gz name either way depending on the edge node.
func maybeGunzip(raw []byte) ([]byte, error) {
	if len(raw) < 2 || raw[0] != 0x1f || raw[1] != 0x8b {
		return raw, nil
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	return io.ReadAll(zr)
}

// drainClose consumes a bounded amount of the body before closing so the
// connection returns to the pool instead of being thrown away.
func drainClose(body io.ReadCloser) {
	_, _ = io.Copy(io.Discard, io.LimitReader(body, 64<<10))
	_ = body.Close()
}
