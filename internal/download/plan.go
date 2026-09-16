package download

// Chunk is a half-open byte range [Start, End) of the target file. End is 0
// when the total size is unknown, meaning "read until EOF".
type Chunk struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`
	Done  int64 `json:"done"`
}

// Len is the chunk's total size, or 0 when the size is unknown.
func (c Chunk) Len() int64 {
	if c.End <= c.Start {
		return 0
	}
	return c.End - c.Start
}

// Remaining is how many bytes of this chunk are still missing.
func (c Chunk) Remaining() int64 {
	if c.End <= c.Start {
		return 1 // unknown size: assume there is more until EOF says otherwise
	}
	return c.End - c.Start - c.Done
}

// minChunk keeps small files from being split into pointlessly many requests.
const minChunk = 8 << 20

// planChunks splits [0, size) into at most connections ranges, covering the
// file exactly: no gaps, no overlaps, the final chunk taking the remainder.
func planChunks(size int64, connections int) []Chunk {
	if size <= 0 {
		return []Chunk{{Start: 0, End: 0}}
	}
	if connections < 1 {
		connections = 1
	}
	n := int64(connections)
	if maxUseful := (size + minChunk - 1) / minChunk; maxUseful < n {
		n = maxUseful
	}
	if n < 1 {
		n = 1
	}

	base, extra := size/n, size%n
	chunks := make([]Chunk, 0, n)
	var offset int64
	for i := int64(0); i < n; i++ {
		length := base
		if i < extra {
			length++ // spread the remainder over the first chunks
		}
		chunks = append(chunks, Chunk{Start: offset, End: offset + length})
		offset += length
	}
	return chunks
}
