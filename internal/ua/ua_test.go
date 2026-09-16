package ua

import "testing"

func TestResolve(t *testing.T) {
	cases := map[string]string{
		"":         Default,
		"default":  Default,
		"browser":  Browser,
		"Chrome":   Browser,
		"curl/8.0": "curl/8.0",
	}
	for in, want := range cases {
		if got := Resolve(in); got != want {
			t.Errorf("Resolve(%q) = %q, want %q", in, got, want)
		}
	}
}
