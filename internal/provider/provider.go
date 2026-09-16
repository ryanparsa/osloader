// Package provider defines the OS-agnostic view of "installers you can get":
// a list of releases per channel, and a resolvable, verifiable artifact for
// each one. macOS is implemented today; Linux and Windows register stubs so
// the UI can show them as coming rather than pretend they do not exist.
package provider

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ryanparsa/osloader/internal/download"
	"github.com/ryanparsa/osloader/internal/verify"
)

// ErrNotImplemented is returned by providers that are registered but not built yet.
var ErrNotImplemented = errors.New("not implemented yet")

// Channel is one release train within a provider (e.g. public vs beta).
type Channel struct {
	ID    string
	Label string
}

// Release is one installer available for download.
type Release struct {
	ID      string    `json:"id"`
	OS      string    `json:"os"`
	Channel string    `json:"channel"`
	Arch    string    `json:"arch,omitempty"`
	Version string    `json:"version"`
	Build   string    `json:"build"`
	Title   string    `json:"title"`
	Posted  time.Time `json:"posted"`
	Size    int64     `json:"size"`
}

// Artifact is the concrete thing to fetch for a Release.
type Artifact struct {
	URL           string   // canonical source
	Mirrors       []string // additional sources for the same bytes, if any
	Filename      string
	Size          int64
	AllowedHosts  []string
	MaxPerHost    int    // connections one host may get; 0 means no limit
	PreferPrimary bool   // use URL, keeping Mirrors as spares rather than extra capacity
	Digest        string // vendor-reported, informational only
	Verifier      verify.Verifier
}

// Target turns an artifact into a download target rooted at dir.
func (a Artifact) Target(dir string) download.Target {
	return download.Target{
		URLs:          append([]string{a.URL}, a.Mirrors...),
		Dest:          filepath.Join(dir, a.Filename),
		Size:          a.Size,
		AllowedHosts:  a.AllowedHosts,
		MaxPerHost:    a.MaxPerHost,
		PreferPrimary: a.PreferPrimary,
		Verifier:      a.Verifier,
	}
}

// Provider lists and resolves installers for one operating system. Facets let
// a provider ask for what it specifically needs - an architecture for Debian,
// nothing at all for macOS - so each OS gets its own menu rather than a shape
// borrowed from another.
type Provider interface {
	Name() string
	Available() bool
	Channels() []Channel
	Facets(channel string) []Facet
	List(ctx context.Context, channel string, sel Selection) ([]Release, error)
	Resolve(ctx context.Context, r Release, sel Selection) (Artifact, error)
}

var (
	mu        sync.RWMutex
	providers = map[string]Provider{}
	order     []string
)

// Register adds a provider under a lowercase key such as "macos".
func Register(key string, p Provider) {
	mu.Lock()
	defer mu.Unlock()
	if _, dup := providers[key]; !dup {
		order = append(order, key)
	}
	providers[key] = p
}

// Get returns the provider registered under key.
func Get(key string) (Provider, error) {
	mu.RLock()
	defer mu.RUnlock()
	p, ok := providers[strings.ToLower(key)]
	if !ok {
		return nil, fmt.Errorf("unknown os %q (have: %s)", key, strings.Join(order, ", "))
	}
	return p, nil
}

// Keys returns registered provider keys in registration order.
func Keys() []string {
	mu.RLock()
	defer mu.RUnlock()
	return append([]string(nil), order...)
}

// Stub is a provider placeholder for an OS that is not implemented yet.
type Stub struct {
	OS          string
	ChannelList []Channel
}

func (s Stub) Name() string          { return s.OS }
func (s Stub) Available() bool       { return false }
func (s Stub) Channels() []Channel   { return s.ChannelList }
func (s Stub) Facets(string) []Facet { return nil }
func (s Stub) List(context.Context, string, Selection) ([]Release, error) {
	return nil, fmt.Errorf("%s: %w", s.OS, ErrNotImplemented)
}
func (s Stub) Resolve(context.Context, Release, Selection) (Artifact, error) {
	return Artifact{}, fmt.Errorf("%s: %w", s.OS, ErrNotImplemented)
}

// SortReleases applies the default order: newest first.
func SortReleases(rs []Release) { Sort(rs, SortDate, false) }

// CompareVersions compares dotted numeric versions component by component, so
// 27.0.1 > 27.0 and 10.15 < 11.0 - never a string comparison.
func CompareVersions(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		var x, y int
		if i < len(as) {
			x, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			y, _ = strconv.Atoi(bs[i])
		}
		if x != y {
			if x > y {
				return 1
			}
			return -1
		}
	}
	return 0
}

// MatchVersion reports whether a release version satisfies a user-supplied
// version selector: "27" matches any 27.x, "27.0" matches 27.0 exactly.
func MatchVersion(release, want string) bool {
	if release == want {
		return true
	}
	return strings.HasPrefix(release, want+".")
}
