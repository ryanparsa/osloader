package download

import (
	"path/filepath"
	"testing"
)

func TestStateRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file.part.json")
	original := &State{
		URL:    "https://swcdn.apple.com/file.pkg",
		Size:   300,
		ETag:   `"abc-1"`,
		Ranges: true,
		Chunks: []Chunk{{Start: 0, End: 100, Done: 50}, {Start: 100, End: 300, Done: 0}},
	}
	if err := save(path, original); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded := loadState(path)
	if loaded == nil {
		t.Fatal("loadState returned nil for a state we just wrote")
	}
	if loaded.URL != original.URL || loaded.ETag != original.ETag || len(loaded.Chunks) != 2 {
		t.Fatalf("round trip lost data: %#v", loaded)
	}
	if loaded.Chunks[0].Done != 50 {
		t.Errorf("chunk progress = %d, want 50", loaded.Chunks[0].Done)
	}
}

// TestStateUsableFor is the safety interlock: saved bytes may only be reused
// when the remote object is provably the same one.
func TestStateUsableFor(t *testing.T) {
	const url = "https://swcdn.apple.com/file.pkg"
	base := func() *State {
		return &State{
			URL:          url,
			Size:         300,
			ETag:         `"abc-1"`,
			LastModified: "Thu, 10 Sep 2026 15:19:04 GMT",
			Ranges:       true,
			Chunks:       []Chunk{{Start: 0, End: 100, Done: 50}, {Start: 100, End: 300}},
		}
	}
	info := remoteInfo{Size: 300, ETag: `"abc-1"`, LastModified: "Thu, 10 Sep 2026 15:19:04 GMT", Ranges: true}

	cases := []struct {
		name  string
		state func() *State
		info  remoteInfo
		want  bool
	}{
		{"unchanged", base, info, true},
		{"missing state", func() *State { return nil }, info, false},
		{"different url", func() *State { s := base(); s.URL = "https://swcdn.apple.com/other.pkg"; return s }, info, false},
		{"etag moved", base, remoteInfo{Size: 300, ETag: `"abc-2"`, Ranges: true}, false},
		{"size moved", base, remoteInfo{Size: 400, ETag: `"abc-1"`, Ranges: true}, false},
		{"last-modified moved", func() *State { s := base(); s.ETag = ""; return s },
			remoteInfo{Size: 300, LastModified: "Fri, 11 Sep 2026 00:00:00 GMT", Ranges: true}, false},
		{"server dropped range support", base, remoteInfo{Size: 300, ETag: `"abc-1"`, Ranges: false}, false},
		{"chunks do not cover the file", func() *State {
			s := base()
			s.Chunks = []Chunk{{Start: 0, End: 100, Done: 50}}
			return s
		}, info, false},
		{"impossible progress", func() *State {
			s := base()
			s.Chunks[0].Done = 999
			return s
		}, info, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.state().usableFor(url, tc.info); got != tc.want {
				t.Errorf("usableFor = %v, want %v", got, tc.want)
			}
		})
	}
}
