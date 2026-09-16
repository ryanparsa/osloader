package download

import "testing"

// TestPlanChunksCoversExactly is the property that matters most: every byte of
// the file belongs to exactly one chunk. A gap means a corrupt download that
// still reports success.
func TestPlanChunksCoversExactly(t *testing.T) {
	sizes := []int64{1, 1023, minChunk - 1, minChunk, minChunk + 1, 100 << 20, 18400314350}
	connections := []int{1, 2, 3, 8, 16}

	for _, size := range sizes {
		for _, conns := range connections {
			chunks := planChunks(size, conns)
			if len(chunks) == 0 {
				t.Fatalf("size=%d conns=%d: no chunks", size, conns)
			}
			if chunks[0].Start != 0 {
				t.Errorf("size=%d conns=%d: first chunk starts at %d", size, conns, chunks[0].Start)
			}
			if last := chunks[len(chunks)-1]; last.End != size {
				t.Errorf("size=%d conns=%d: last chunk ends at %d, want %d", size, conns, last.End, size)
			}
			if len(chunks) > conns {
				t.Errorf("size=%d conns=%d: planned %d chunks", size, conns, len(chunks))
			}
			var covered int64
			for i, c := range chunks {
				if c.End <= c.Start {
					t.Errorf("size=%d conns=%d: chunk %d is empty (%d-%d)", size, conns, i, c.Start, c.End)
				}
				if i > 0 && c.Start != chunks[i-1].End {
					t.Errorf("size=%d conns=%d: chunk %d starts at %d, previous ended at %d",
						size, conns, i, c.Start, chunks[i-1].End)
				}
				covered += c.Len()
			}
			if covered != size {
				t.Errorf("size=%d conns=%d: chunks cover %d bytes", size, conns, covered)
			}
		}
	}
}

// TestPlanChunksKeepsSmallFilesWhole avoids splitting a file into more requests
// than it has meaningful pieces.
func TestPlanChunksKeepsSmallFilesWhole(t *testing.T) {
	if got := planChunks(1<<20, 8); len(got) != 1 {
		t.Errorf("1 MiB over 8 connections planned %d chunks, want 1", len(got))
	}
	if got := planChunks(64<<20, 8); len(got) != 8 {
		t.Errorf("64 MiB over 8 connections planned %d chunks, want 8", len(got))
	}
}

// TestPlanChunksUnknownSize falls back to one open-ended chunk.
func TestPlanChunksUnknownSize(t *testing.T) {
	chunks := planChunks(0, 8)
	if len(chunks) != 1 || chunks[0].Len() != 0 {
		t.Fatalf("unknown size planned %#v", chunks)
	}
	if chunks[0].Remaining() <= 0 {
		t.Error("an unknown-size chunk should always look unfinished")
	}
}
