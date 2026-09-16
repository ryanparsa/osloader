package download

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// State is the sidecar written next to the partial file. It is what makes a
// half-finished 17 GiB download worth keeping: on restart it says which byte
// ranges already landed, and which version of the remote file they came from.
type State struct {
	URL          string    `json:"url"`
	Size         int64     `json:"size"`
	ETag         string    `json:"etag,omitempty"`
	LastModified string    `json:"last_modified,omitempty"`
	Ranges       bool      `json:"ranges"`
	Chunks       []Chunk   `json:"chunks"`
	Updated      time.Time `json:"updated"`
}

// loadState reads a sidecar; a missing or unreadable file simply means "start over".
func loadState(path string) *State {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var s State
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil
	}
	if len(s.Chunks) == 0 {
		return nil
	}
	return &s
}

// usableFor reports whether saved progress still describes the remote file. If
// the server's ETag, modification time or size moved, the bytes on disk belong
// to a different file and must be discarded rather than silently mixed in.
func (s *State) usableFor(url string, info remoteInfo) bool {
	switch {
	case s == nil, s.URL != url, !info.Ranges, !s.Ranges:
		return false
	case info.Size > 0 && s.Size != info.Size:
		return false
	case s.ETag != "" && info.ETag != "" && s.ETag != info.ETag:
		return false
	case s.LastModified != "" && info.LastModified != "" && s.LastModified != info.LastModified:
		return false
	}
	// Chunk bookkeeping must be internally consistent, or we cannot trust it.
	var covered int64
	for _, c := range s.Chunks {
		if c.Done < 0 || c.End < c.Start || c.Done > c.Len() {
			return false
		}
		covered += c.Len()
	}
	return s.Size <= 0 || covered == s.Size
}

// save writes the sidecar atomically: a crash mid-write must not leave behind a
// truncated state file that would strand an otherwise resumable download.
func save(path string, s *State) error {
	s.Updated = time.Now().UTC()
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".osloader-state-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
