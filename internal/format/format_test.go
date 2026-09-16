package format

import (
	"testing"
	"time"
)

func TestHumanBytes(t *testing.T) {
	cases := map[int64]string{
		0:           "0 B",
		512:         "512 B",
		1024:        "1.0 KiB",
		18400314350: "17.1 GiB",
	}
	for in, want := range cases {
		if got := HumanBytes(in); got != want {
			t.Errorf("HumanBytes(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestParseRate(t *testing.T) {
	cases := map[string]int64{
		"":       0,
		"500k":   500 << 10,
		"20M":    20 << 20,
		"1.5MiB": 1024 * 1024 * 3 / 2,
		"2G":     2 << 30,
	}
	for in, want := range cases {
		got, err := ParseRate(in)
		if err != nil {
			t.Errorf("ParseRate(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ParseRate(%q) = %d, want %d", in, got, want)
		}
	}
	if _, err := ParseRate("fast"); err == nil {
		t.Error("ParseRate should reject nonsense")
	}
}

func TestHumanDuration(t *testing.T) {
	cases := map[time.Duration]string{
		0:                           "—",
		45 * time.Second:            "45s",
		83 * time.Second:            "1m23s",
		3*time.Hour + 4*time.Minute: "3h04m",
	}
	for in, want := range cases {
		if got := HumanDuration(in); got != want {
			t.Errorf("HumanDuration(%s) = %q, want %q", in, got, want)
		}
	}
}
