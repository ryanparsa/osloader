package debian

import (
	"context"
	"strings"
	"sync"

	"github.com/ryanparsa/osloader/internal/provider/webdir"
)

// masterlist is Debian's own machine-readable list of mirrors. Using it means
// the menu offers the mirrors Debian actually publishes rather than a handful
// hard-coded here.
const masterlist = "https://mirror-master.debian.org/status/Mirrors.masterlist"

// mirrorSite is one mirror that carries CD images.
type mirrorSite struct {
	Host    string // "ftp.acc.umu.se"
	Country string // "Sweden"
	City    string // "Umeå"
	Path    string // "/debian-cd/"
}

// Label is how the mirror reads in a menu.
func (m mirrorSite) Label() string {
	where := m.Country
	if m.City != "" && m.Country != "" {
		where = m.City + ", " + m.Country
	}
	if where == "" {
		return m.Host
	}
	return m.Host + " - " + where
}

// URL builds the address of one image on this mirror. Paths come from the
// catalogue rooted at cdimage.debian.org, while mirrors publish the same tree
// under their own CDImage-http prefix.
func (m mirrorSite) URL(path string) (string, bool) {
	rest, ok := strings.CutPrefix(path, "debian-cd/")
	if !ok {
		return "", false // weekly builds live only on Debian's own server
	}
	return "https://" + m.Host + m.Path + rest, true
}

// parseMasterlist reads the RFC822-style stanzas, keeping the sites that
// publish CD images.
func parseMasterlist(body string) []mirrorSite {
	var sites []mirrorSite

	for _, stanza := range strings.Split(body, "\n\n") {
		var site mirrorSite
		for _, line := range strings.Split(stanza, "\n") {
			key, value, found := strings.Cut(line, ":")
			if !found {
				continue
			}
			value = strings.TrimSpace(value)
			switch strings.TrimSpace(key) {
			case "Site":
				site.Host = value
			case "Country":
				// "SE Sweden" - the code adds nothing a person needs.
				if _, name, ok := strings.Cut(value, " "); ok {
					site.Country = name
				} else {
					site.Country = value
				}
			case "Location":
				site.City = value
			case "CDImage-http":
				site.Path = value
			}
		}
		if site.Host == "" || site.Path == "" || site.Host == "cdimage.debian.org" {
			continue
		}
		if !strings.HasSuffix(site.Path, "/") {
			site.Path += "/"
		}
		sites = append(sites, site)
	}
	return sites
}

// mirrorCache holds the parsed masterlist for the life of the process. A failed
// fetch is not cached, so a menu opened offline can still fill in later.
type mirrorCache struct {
	mu     sync.Mutex
	loaded bool
	sites  []mirrorSite
}

// sites fetches Debian's mirror list, once, and returns what it knows. A
// failure is not fatal: the menu still offers Debian's own server, which is
// the default anyway.
func (p *Provider) sites(ctx context.Context) []mirrorSite {
	p.mirrors.mu.Lock()
	defer p.mirrors.mu.Unlock()
	if p.mirrors.loaded {
		return p.mirrors.sites
	}

	body, err := webdir.FetchText(ctx, p.client, masterlist, p.agent, p.logger)
	if err != nil {
		p.logger.Warn("mirror list unavailable", "err", err.Error())
		return nil
	}
	p.mirrors.sites = parseMasterlist(body)
	p.mirrors.loaded = true
	p.logger.Info("mirror list", "mirrors", len(p.mirrors.sites))
	return p.mirrors.sites
}

// siteByHost finds a mirror by the host the user named.
func siteByHost(sites []mirrorSite, host string) (mirrorSite, bool) {
	for _, site := range sites {
		if strings.EqualFold(site.Host, host) {
			return site, true
		}
	}
	return mirrorSite{}, false
}
