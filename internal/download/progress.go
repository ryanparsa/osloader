package download

import (
	"sync"
	"sync/atomic"
	"time"
)

// WorkerSnapshot is what one connection is doing right now.
type WorkerSnapshot struct {
	Bytes      int64   // total bytes this connection has written
	Speed      float64 // bytes/second, smoothed
	Chunk      int     // index of the chunk in flight, -1 when idle
	ChunkDone  int64
	ChunkTotal int64
	Mirror     string // host currently being read from
}

// Active reports whether the connection is currently fetching a chunk.
func (w WorkerSnapshot) Active() bool { return w.Chunk >= 0 }

// Fraction is how far through its current chunk the connection is.
func (w WorkerSnapshot) Fraction() float64 {
	if w.ChunkTotal <= 0 {
		return 0
	}
	if w.ChunkDone >= w.ChunkTotal {
		return 1
	}
	return float64(w.ChunkDone) / float64(w.ChunkTotal)
}

// Snapshot is a consistent view of transfer progress for a UI to render.
type Snapshot struct {
	Total   int64
	Done    int64
	Speed   float64 // bytes/second, smoothed
	ETA     time.Duration
	Elapsed time.Duration
	Workers []WorkerSnapshot
}

// Fraction is progress in [0,1]; it is 0 when the total size is unknown.
func (s Snapshot) Fraction() float64 {
	if s.Total <= 0 {
		return 0
	}
	if s.Done >= s.Total {
		return 1
	}
	return float64(s.Done) / float64(s.Total)
}

// worker holds the counters for one connection.
type worker struct {
	bytes  atomic.Int64
	chunk  atomic.Int64 // 1-based chunk index, 0 when idle
	mirror atomic.Pointer[string]
}

// sample is the previous reading used to derive a speed.
type sample struct {
	at    time.Time
	bytes int64
	speed float64
}

// Progress counts bytes with atomics rather than messages: a per-write channel
// send would cost more than the copy itself and would grow unboundedly if the
// UI fell behind. Readers poll a snapshot instead.
type Progress struct {
	total     int64
	chunkLens []int64
	chunks    []atomic.Int64
	workers   []worker
	start     time.Time

	mu      sync.Mutex
	overall sample
	perWork []sample
}

func newProgress(total int64, chunkLens []int64, workers int) *Progress {
	now := time.Now()
	return &Progress{
		total:     total,
		chunkLens: chunkLens,
		chunks:    make([]atomic.Int64, len(chunkLens)),
		workers:   make([]worker, workers),
		start:     now,
		overall:   sample{at: now},
		perWork:   make([]sample, workers),
	}
}

// add records bytes written for a chunk by a worker.
func (p *Progress) add(chunk, worker int, n int64) {
	if n <= 0 {
		return
	}
	p.chunks[chunk].Add(n)
	if worker >= 0 && worker < len(p.workers) {
		p.workers[worker].bytes.Add(n)
	}
}

// set replaces a chunk's count, used when resuming from saved state.
func (p *Progress) set(chunk int, n int64) { p.chunks[chunk].Store(n) }

// claim records which chunk a connection has picked up, and release marks it
// idle again - this is what lets the UI show a stalled connection.
func (p *Progress) claim(worker, chunk int) {
	if worker >= 0 && worker < len(p.workers) {
		p.workers[worker].chunk.Store(int64(chunk) + 1)
	}
}

// useMirror records which host a connection is reading from, so a UI can show
// where each connection is pulling from when a file has several mirrors.
func (p *Progress) useMirror(worker int, host string) {
	if worker >= 0 && worker < len(p.workers) {
		p.workers[worker].mirror.Store(&host)
	}
}

func (p *Progress) release(worker int) {
	if worker >= 0 && worker < len(p.workers) {
		p.workers[worker].chunk.Store(0)
	}
}

// chunkDone is how much of one chunk has landed.
func (p *Progress) chunkDone(chunk int) int64 { return p.chunks[chunk].Load() }

// Done is the total number of bytes written so far.
func (p *Progress) Done() int64 {
	var sum int64
	for i := range p.chunks {
		sum += p.chunks[i].Load()
	}
	return sum
}

// Snapshot samples the counters and updates the smoothed speeds. The averages
// are exponential so a stalled connection shows up quickly without the readout
// jittering on every sample.
func (p *Progress) Snapshot() Snapshot {
	done := p.Done()
	now := time.Now()

	p.mu.Lock()
	speed := smooth(&p.overall, done, now)
	workers := make([]WorkerSnapshot, len(p.workers))
	for i := range p.workers {
		bytes := p.workers[i].bytes.Load()
		w := WorkerSnapshot{
			Bytes: bytes,
			Speed: smooth(&p.perWork[i], bytes, now),
			Chunk: int(p.workers[i].chunk.Load()) - 1,
		}
		if host := p.workers[i].mirror.Load(); host != nil {
			w.Mirror = *host
		}
		if w.Chunk >= 0 && w.Chunk < len(p.chunkLens) {
			w.ChunkDone = p.chunks[w.Chunk].Load()
			w.ChunkTotal = p.chunkLens[w.Chunk]
		}
		workers[i] = w
	}
	p.mu.Unlock()

	snap := Snapshot{
		Total:   p.total,
		Done:    done,
		Speed:   speed,
		Elapsed: now.Sub(p.start),
		Workers: workers,
	}
	if speed > 0 && p.total > done {
		snap.ETA = time.Duration(float64(p.total-done) / speed * float64(time.Second))
	}
	return snap
}

// smooth advances an exponential moving average, leaving the previous value
// alone if too little time has passed to measure anything meaningful.
func smooth(s *sample, bytes int64, now time.Time) float64 {
	elapsed := now.Sub(s.at)
	if elapsed < 100*time.Millisecond {
		return s.speed
	}
	instant := float64(bytes-s.bytes) / elapsed.Seconds()
	if s.speed == 0 {
		s.speed = instant
	} else {
		s.speed = 0.3*instant + 0.7*s.speed
	}
	s.at, s.bytes = now, bytes
	return s.speed
}
