package provider

import (
	"fmt"
	"sort"
	"strings"
)

// SortMode is a way of ordering a release list.
type SortMode string

const (
	// SortDate is newest first — the default, and what most people want.
	SortDate SortMode = "date"
	// SortVersion is highest version first, compared numerically.
	SortVersion SortMode = "version"
	// SortSize is largest download first.
	SortSize SortMode = "size"
	// SortName is alphabetical by title, A to Z.
	SortName SortMode = "name"
)

// SortModes lists the modes in the order the TUI cycles through them.
var SortModes = []SortMode{SortDate, SortVersion, SortSize, SortName}

// Label describes the mode and the direction it sorts by default.
func (m SortMode) Label() string {
	switch m {
	case SortVersion:
		return "version, newest first"
	case SortSize:
		return "size, largest first"
	case SortName:
		return "name, A to Z"
	default:
		return "date posted, newest first"
	}
}

// Short is the one-word form for a compact header.
func (m SortMode) Short() string {
	if m == "" {
		return string(SortDate)
	}
	return string(m)
}

// Next returns the following mode, wrapping around.
func (m SortMode) Next() SortMode {
	for i, mode := range SortModes {
		if mode == m {
			return SortModes[(i+1)%len(SortModes)]
		}
	}
	return SortModes[0]
}

// ParseSortMode validates a user-supplied sort name.
func ParseSortMode(s string) (SortMode, error) {
	mode := SortMode(strings.ToLower(strings.TrimSpace(s)))
	if mode == "" {
		return SortDate, nil
	}
	for _, known := range SortModes {
		if mode == known {
			return mode, nil
		}
	}
	names := make([]string, len(SortModes))
	for i, known := range SortModes {
		names[i] = string(known)
	}
	return "", fmt.Errorf("unknown sort %q (have: %s)", s, strings.Join(names, ", "))
}

// Sort orders releases in place. Each mode has a natural direction — newest,
// largest, A to Z — and reverse flips whichever one applies.
func Sort(rs []Release, mode SortMode, reverse bool) {
	less := lessFor(mode)
	sort.SliceStable(rs, func(i, j int) bool {
		if reverse {
			return less(rs[j], rs[i])
		}
		return less(rs[i], rs[j])
	})
}

// lessFor builds the comparison for a mode, falling back to date so equal keys
// still come out in a stable, sensible order.
func lessFor(mode SortMode) func(a, b Release) bool {
	byDate := func(a, b Release) bool {
		if !a.Posted.Equal(b.Posted) {
			return a.Posted.After(b.Posted)
		}
		return CompareVersions(a.Version, b.Version) > 0
	}

	switch mode {
	case SortVersion:
		return func(a, b Release) bool {
			if cmp := CompareVersions(a.Version, b.Version); cmp != 0 {
				return cmp > 0
			}
			return byDate(a, b)
		}
	case SortSize:
		return func(a, b Release) bool {
			if a.Size != b.Size {
				return a.Size > b.Size
			}
			return byDate(a, b)
		}
	case SortName:
		return func(a, b Release) bool {
			ta, tb := strings.ToLower(a.Title), strings.ToLower(b.Title)
			if ta != tb {
				return ta < tb
			}
			return byDate(a, b)
		}
	default:
		return byDate
	}
}
