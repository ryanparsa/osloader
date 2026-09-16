package provider

import (
	"testing"
	"time"
)

func day(d int) time.Time { return time.Date(2026, 9, d, 0, 0, 0, 0, time.UTC) }

func sample() []Release {
	return []Release{
		{ID: "old-big", Version: "26.7", Title: "macOS Tahoe", Posted: day(1), Size: 17 << 30},
		{ID: "new-small", Version: "15.8", Title: "macOS Sequoia", Posted: day(14), Size: 14 << 30},
		{ID: "newest", Version: "27.0", Title: "macOS Golden Gate", Posted: day(14), Size: 17 << 30},
		{ID: "oldest", Version: "11.7.11", Title: "macOS Big Sur", Posted: day(2), Size: 11 << 30},
	}
}

func ids(rs []Release) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.ID
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestSortModes pins each ordering, including the tie-break: two releases
// posted the same day fall back to version, so "newest" is never arbitrary.
func TestSortModes(t *testing.T) {
	cases := []struct {
		mode    SortMode
		reverse bool
		want    []string
	}{
		{SortDate, false, []string{"newest", "new-small", "oldest", "old-big"}},
		{SortDate, true, []string{"old-big", "oldest", "new-small", "newest"}},
		{SortVersion, false, []string{"newest", "old-big", "new-small", "oldest"}},
		{SortVersion, true, []string{"oldest", "new-small", "old-big", "newest"}},
		{SortSize, false, []string{"newest", "old-big", "new-small", "oldest"}},
		{SortName, false, []string{"oldest", "newest", "new-small", "old-big"}},
		{SortName, true, []string{"old-big", "new-small", "newest", "oldest"}},
	}

	for _, tc := range cases {
		releases := sample()
		Sort(releases, tc.mode, tc.reverse)
		if got := ids(releases); !equal(got, tc.want) {
			t.Errorf("Sort(%s, reverse=%v) = %v, want %v", tc.mode, tc.reverse, got, tc.want)
		}
	}
}

// TestSortVersionIsNumeric: 11.7.11 must not outrank 15.8 as a string would.
func TestSortVersionIsNumeric(t *testing.T) {
	releases := []Release{{ID: "a", Version: "9.1"}, {ID: "b", Version: "11.7.11"}, {ID: "c", Version: "15.8"}}
	Sort(releases, SortVersion, false)
	if got := ids(releases); !equal(got, []string{"c", "b", "a"}) {
		t.Errorf("version order = %v, want [c b a]", got)
	}
}

func TestSortModeCycleAndParse(t *testing.T) {
	mode := SortDate
	seen := map[SortMode]bool{}
	for range SortModes {
		seen[mode] = true
		mode = mode.Next()
	}
	if len(seen) != len(SortModes) {
		t.Errorf("cycling visited %d of %d modes", len(seen), len(SortModes))
	}
	if mode != SortDate {
		t.Errorf("cycling did not wrap back to the default, ended at %s", mode)
	}

	if got, err := ParseSortMode("VERSION"); err != nil || got != SortVersion {
		t.Errorf("ParseSortMode(\"VERSION\") = %q, %v", got, err)
	}
	if got, err := ParseSortMode(""); err != nil || got != SortDate {
		t.Errorf("empty sort should default to date, got %q, %v", got, err)
	}
	if _, err := ParseSortMode("colour"); err == nil {
		t.Error("an unknown sort should be rejected")
	}
}
