package tui

import (
	"github.com/charmbracelet/bubbles/list"

	"github.com/ryanparsa/osloader/internal/provider"
)

// screen is everything needed to put a page back exactly as it was: the list
// with its items and cursor, plus the context that page was showing.
type screen struct {
	state     state
	list      list.Model
	prov      provider.Provider
	provKey   string
	channel   string
	releases  []provider.Release
	allFacets []provider.Facet
	facets    []provider.Facet
	facetAt   int
	selection provider.Selection
	sortMode  provider.SortMode
	sortDesc  bool
	release   provider.Release
}

func (m model) capture() screen {
	return screen{
		state:     m.state,
		list:      m.list,
		prov:      m.prov,
		provKey:   m.provKey,
		channel:   m.channel,
		releases:  m.releases,
		allFacets: m.allFacets,
		facets:    m.facets,
		facetAt:   m.facetAt,
		selection: m.selection,
		sortMode:  m.sortMode,
		sortDesc:  m.sortDesc,
		release:   m.release,
	}
}

func (m model) restore(s screen) model {
	m.state = s.state
	m.list = s.list
	m.prov = s.prov
	m.provKey = s.provKey
	m.channel = s.channel
	m.releases = s.releases
	m.allFacets = s.allFacets
	m.facets = s.facets
	m.facetAt = s.facetAt
	m.selection = s.selection
	m.sortMode = s.sortMode
	m.sortDesc = s.sortDesc
	m.release = s.release

	// Whatever was being loaded or shown for the page we left is no longer
	// current; a stale reply must not drag us forward again.
	m.gen++
	m.loading = false
	m.status = ""
	m.outcome, m.err, m.resumable = nil, nil, false
	m.list.SetSize(m.listWidth(), m.listHeight())
	return m
}

// push records the current page before moving to a new one. Like a browser,
// navigating forward from here discards any page we had gone back from.
func (m model) push() model {
	m.back = append(m.back, m.capture())
	m.forward = nil
	m.gen++
	return m
}

// canGoBack reports whether there is a previous page to return to. A download
// in flight is deliberately not navigable: q stops it first.
func (m model) canGoBack() bool {
	return len(m.back) > 0 && m.state != stateDownloading
}

func (m model) canGoForward() bool {
	return len(m.forward) > 0 && m.state != stateDownloading
}

func (m model) goBack() model {
	if !m.canGoBack() {
		return m
	}
	previous := m.back[len(m.back)-1]
	m.back = m.back[:len(m.back)-1]
	current := m.capture()
	m = m.restore(previous)
	m.forward = append(m.forward, current)
	return m
}

func (m model) goForward() model {
	if !m.canGoForward() {
		return m
	}
	next := m.forward[len(m.forward)-1]
	m.forward = m.forward[:len(m.forward)-1]
	current := m.capture()
	m = m.restore(next)
	m.back = append(m.back, current)
	return m
}
