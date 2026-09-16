package provider

import (
	"fmt"
	"sort"
	"strings"
)

// FacetValue is one option within a facet.
type FacetValue struct {
	ID    string
	Label string
}

// FacetStage says when a question is worth asking.
type FacetStage int

const (
	// StageList shapes the list itself: an architecture changes what there is
	// to choose from, so it is asked first.
	StageList FacetStage = iota
	// StageResolve shapes how a chosen release is fetched - which mirror, for
	// instance - so it is asked once the user has picked something.
	StageResolve
)

// Facet is an extra choice a provider needs before it can show a useful list.
// Operating systems differ here: Apple ships one installer per release, Debian
// builds every image for several architectures, and Windows multiplies edition
// by language by architecture. Rather than pretend they are the same, each
// provider declares the questions it needs answered and the UI asks them.
type Facet struct {
	Key      string // "arch"
	Label    string // "Architecture"
	Stage    FacetStage
	AllowAll bool // offer "no preference", listing everything
	FreeForm bool // Values are suggestions; any value is accepted
	Values   []FacetValue
}

// FacetsAt returns the questions for one stage, in declared order.
func FacetsAt(facets []Facet, stage FacetStage) []Facet {
	var out []Facet
	for _, f := range facets {
		if f.Stage == stage {
			out = append(out, f)
		}
	}
	return out
}

// Selection is the answers to a provider's facets, keyed by facet key. A
// missing or empty value means "no preference".
type Selection map[string]string

// Get returns the chosen value for a facet, or "" for no preference.
func (s Selection) Get(key string) string {
	if s == nil {
		return ""
	}
	return s[key]
}

// With returns a copy with one facet set, so callers never mutate a shared map.
func (s Selection) With(key, value string) Selection {
	next := make(Selection, len(s)+1)
	for k, v := range s {
		next[k] = v
	}
	if value == "" {
		delete(next, key)
	} else {
		next[key] = value
	}
	return next
}

// String renders the selection deterministically: it is used for cache keys,
// log lines and the shareable command, all of which need a stable order.
func (s Selection) String() string {
	if len(s) == 0 {
		return ""
	}
	keys := make([]string, 0, len(s))
	for k, v := range s {
		if v != "" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+s[k])
	}
	return strings.Join(parts, " ")
}

// ParseFilter reads a "key=value" pair from the command line.
func ParseFilter(raw string) (key, value string, err error) {
	key, value, found := strings.Cut(raw, "=")
	key, value = strings.TrimSpace(key), strings.TrimSpace(value)
	if !found || key == "" || value == "" {
		return "", "", fmt.Errorf("invalid filter %q: use key=value, e.g. arch=amd64", raw)
	}
	return key, value, nil
}

// Validate checks a selection against the facets a provider declared, so a
// typo in --filter is reported rather than silently ignored.
func Validate(facets []Facet, sel Selection) error {
	for key, value := range sel {
		if value == "" {
			continue
		}
		facet, ok := findFacet(facets, key)
		if !ok {
			return fmt.Errorf("unknown filter %q (have: %s)", key, facetKeys(facets))
		}
		if facet.FreeForm {
			continue
		}
		if !hasValue(facet, value) {
			return fmt.Errorf("unknown %s %q (have: %s)", key, value, facetValues(facet))
		}
	}
	return nil
}

func findFacet(facets []Facet, key string) (Facet, bool) {
	for _, f := range facets {
		if f.Key == key {
			return f, true
		}
	}
	return Facet{}, false
}

func hasValue(f Facet, value string) bool {
	for _, v := range f.Values {
		if v.ID == value {
			return true
		}
	}
	return false
}

func facetKeys(facets []Facet) string {
	if len(facets) == 0 {
		return "none for this OS"
	}
	keys := make([]string, 0, len(facets))
	for _, f := range facets {
		keys = append(keys, f.Key)
	}
	return strings.Join(keys, ", ")
}

func facetValues(f Facet) string {
	values := make([]string, 0, len(f.Values))
	for _, v := range f.Values {
		values = append(values, v.ID)
	}
	return strings.Join(values, ", ")
}
