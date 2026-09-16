package provider

import "testing"

var debianFacets = []Facet{
	{Key: "arch", Label: "Architecture", AllowAll: true, Values: []FacetValue{
		{ID: "amd64"}, {ID: "arm64"}, {ID: "i386"},
	}},
	{Key: "media", Label: "Image size", AllowAll: true, Values: []FacetValue{
		{ID: "cd"}, {ID: "dvd"},
	}},
}

func TestSelectionWithIsACopy(t *testing.T) {
	base := Selection{"arch": "amd64"}
	next := base.With("media", "dvd")

	if base.Get("media") != "" {
		t.Error("With mutated the original selection")
	}
	if next.Get("arch") != "amd64" || next.Get("media") != "dvd" {
		t.Errorf("next = %v", next)
	}
	// An empty value means "no preference" and drops the key.
	if cleared := next.With("media", ""); cleared.Get("media") != "" {
		t.Error("setting an empty value should clear the facet")
	}
	if (Selection(nil)).Get("arch") != "" {
		t.Error("a nil selection should read as empty")
	}
}

func TestSelectionStringIsStable(t *testing.T) {
	sel := Selection{"media": "cd", "arch": "amd64", "empty": ""}
	if got, want := sel.String(), "arch=amd64 media=cd"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
	if got := (Selection{}).String(); got != "" {
		t.Errorf("empty selection = %q", got)
	}
}

func TestParseFilter(t *testing.T) {
	key, value, err := ParseFilter(" arch = amd64 ")
	if err != nil || key != "arch" || value != "amd64" {
		t.Errorf("ParseFilter = %q,%q,%v", key, value, err)
	}
	for _, bad := range []string{"arch", "=amd64", "arch=", ""} {
		if _, _, err := ParseFilter(bad); err == nil {
			t.Errorf("ParseFilter(%q) should fail", bad)
		}
	}
}

// TestValidate catches typos instead of silently listing the wrong thing.
func TestValidate(t *testing.T) {
	if err := Validate(debianFacets, Selection{"arch": "amd64", "media": "dvd"}); err != nil {
		t.Errorf("valid selection rejected: %v", err)
	}
	if err := Validate(debianFacets, Selection{"arch": ""}); err != nil {
		t.Errorf("an empty value means no preference: %v", err)
	}
	if err := Validate(debianFacets, Selection{"arch": "sparc"}); err == nil {
		t.Error("an unknown value should be rejected")
	}
	if err := Validate(debianFacets, Selection{"cpu": "arm"}); err == nil {
		t.Error("an unknown facet should be rejected")
	}
	if err := Validate(nil, Selection{"arch": "amd64"}); err == nil {
		t.Error("filtering an OS with no facets should be rejected")
	}
}
