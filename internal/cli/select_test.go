package cli

import (
	"testing"

	"github.com/ryanparsa/osloader/internal/provider"
)

var goldenGate = provider.Release{ID: "142-15488", Version: "27.0", Build: "26A428"}

// TestSelectorRequiresEveryField: the shareable command line prints product,
// version and build together, so all of them have to agree. If Apple ever
// reuses a product ID for a different build, that command must fail rather
// than fetch the wrong file.
func TestSelectorRequiresEveryField(t *testing.T) {
	cases := []struct {
		name  string
		sel   selector
		match bool
	}{
		{"product only", selector{product: "142-15488"}, true},
		{"build only", selector{build: "26A428"}, true},
		{"version only", selector{version: "27.0"}, true},
		{"major version only", selector{version: "27"}, true},
		{"all three agreeing", selector{product: "142-15488", version: "27.0", build: "26A428"}, true},
		{"build disagrees", selector{product: "142-15488", build: "26A429"}, false},
		{"product disagrees", selector{product: "142-99999", build: "26A428"}, false},
		{"version disagrees", selector{version: "26.7", build: "26A428"}, false},
		{"wrong minor", selector{version: "27.1"}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.sel.matches(goldenGate); got != tc.match {
				t.Errorf("matches = %v, want %v", got, tc.match)
			}
		})
	}
}

func TestSelectorEmptyAndString(t *testing.T) {
	if !(selector{}).empty() {
		t.Error("a selector with no fields should be empty")
	}
	if (selector{build: "26A428"}).empty() {
		t.Error("a selector with a build is not empty")
	}

	sel := selector{product: "142-15488", version: "27.0", build: "26A428"}
	if got, want := sel.String(), "--product 142-15488 --version 27.0 --build 26A428"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}
