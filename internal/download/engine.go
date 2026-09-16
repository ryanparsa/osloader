// Package download implements a concurrent, resumable file transfer: the file
// is split into byte ranges fetched in parallel and written straight to their
// offsets on disk, with a sidecar recording what has landed so an interrupted
// run picks up where it stopped. Memory use is fixed — one buffer per worker,
// never the file.
package download

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"

	"github.com/ryanparsa/osloader/internal/logging"
	"github.com/ryanparsa/osloader/internal/ua"
)

const (
	// bufSize is the per-worker copy buffer. This, times the worker count, is
	// the transfer's entire memory footprint.
	bufSize = 256 << 10

	// DefaultConnections is the parallelism used when nothing else is asked for.
	DefaultConnections = 8
	maxConnections     = 16
	defaultAttempts    = 5
	defaultStall       = 60 * time.Second
)

// Options configures an Engine.
type Options struct {
	URLs          []string // mirrors of the same file; the first is the primary
	Dest          string   // final path; the partial file lives alongside it
	Size          int64    // expected size from the vendor catalog, 0 if unknown
	Connections   int      // parallel range requests
	AllowedHosts  []string // empty means "any host" (tests only)
	LimitRate     int64    // bytes/second, 0 for unlimited
	UserAgent     string
	StallTimeout  time.Duration // abort a connection that goes quiet this long
	MaxAttempts   int           // per-chunk attempts before giving up
	MaxPerHost    int           // connections one host may get; 0 means no limit
	PreferPrimary bool          // use the first mirror, keeping the rest as spares
	Logger        *slog.Logger  // verbose event log; nil discards
}

func (o *Options) applyDefaults() {
	if o.Connections <= 0 {
		o.Connections = DefaultConnections
	}
	if o.Connections > maxConnections {
		o.Connections = maxConnections
	}
	if o.MaxAttempts <= 0 {
		o.MaxAttempts = defaultAttempts
	}
	if o.StallTimeout <= 0 {
		o.StallTimeout = defaultStall
	}
	if o.UserAgent == "" {
		o.UserAgent = ua.Default
	}
	if o.Logger == nil {
		o.Logger = logging.Discard()
	}
}

// Engine runs one transfer.
type Engine struct {
	opts      Options
	mirrors   []*mirror
	client    *http.Client
	limiter   *rate.Limiter
	bufs      sync.Pool
	prog      atomic.Pointer[Progress]
	partPath  string
	statePath string

	stateMu sync.Mutex
	state   *State
	file    *os.File
}

// New validates the options and prepares an engine; nothing is fetched yet.
func New(opts Options) (*Engine, error) {
	opts.applyDefaults()
	if len(opts.URLs) == 0 || opts.Dest == "" {
		return nil, errors.New("download: at least one URL and a Dest are required")
	}

	mirrors := make([]*mirror, 0, len(opts.URLs))
	for _, raw := range opts.URLs {
		if err := checkURL(raw, opts.AllowedHosts); err != nil {
			return nil, err
		}
		m, err := newMirror(raw)
		if err != nil {
			return nil, err
		}
		mirrors = append(mirrors, m)
	}

	e := &Engine{
		mirrors:   mirrors,
		opts:      opts,
		client:    newClient(opts.Connections, opts.AllowedHosts),
		partPath:  opts.Dest + ".part",
		statePath: opts.Dest + ".part.json",
		bufs: sync.Pool{New: func() any {
			buf := make([]byte, bufSize)
			return &buf
		}},
	}
	if opts.LimitRate > 0 {
		e.limiter = rate.NewLimiter(rate.Limit(opts.LimitRate), bufSize)
	}
	return e, nil
}

// log is the verbose event log; it is never nil.
func (e *Engine) log() *slog.Logger { return e.opts.Logger }

// PartPath is where bytes accumulate until the download is verified.
func (e *Engine) PartPath() string { return e.partPath }

// StatePath is the resume sidecar.
func (e *Engine) StatePath() string { return e.statePath }

// Snapshot is safe to call at any time, including before Run has planned.
func (e *Engine) Snapshot() Snapshot {
	if p := e.prog.Load(); p != nil {
		return p.Snapshot()
	}
	return Snapshot{Total: e.opts.Size}
}

// remoteInfo is what the pre-flight request tells us about the target.
type remoteInfo struct {
	Size         int64
	ETag         string
	LastModified string
	Ranges       bool
}

// preflight asks the server what we are about to download, and whether it will
// serve byte ranges. Some servers refuse HEAD, so a one-byte ranged GET is the
// fallback — it answers both questions at once.
func (e *Engine) preflight(ctx context.Context) (remoteInfo, error) {
	info, err := e.probe(ctx, e.primary().url)
	if err != nil {
		return remoteInfo{}, err
	}
	e.primary().etag = info.ETag
	if len(e.mirrors) > 1 {
		e.checkMirrors(ctx, info)
	}
	return info, nil
}

// probe asks one mirror what it would serve, and whether it serves ranges.
func (e *Engine) probe(ctx context.Context, url string) (remoteInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return remoteInfo{}, err
	}
	req.Header.Set("User-Agent", e.opts.UserAgent)

	resp, err := e.client.Do(req)
	if err == nil {
		defer drainClose(resp.Body)
		if resp.StatusCode == http.StatusOK {
			info := remoteInfo{
				Size:         resp.ContentLength,
				ETag:         resp.Header.Get("ETag"),
				LastModified: resp.Header.Get("Last-Modified"),
				Ranges:       strings.EqualFold(resp.Header.Get("Accept-Ranges"), "bytes"),
			}
			e.log().Info("preflight", "url", url, "method", "HEAD", "status", resp.StatusCode,
				"size", info.Size, "ranges", info.Ranges, "etag", info.ETag)
			return info, nil
		}
		if resp.StatusCode != http.StatusMethodNotAllowed && resp.StatusCode != http.StatusForbidden {
			return remoteInfo{}, &httpError{status: resp.StatusCode, url: url}
		}
	}
	return e.probeByRange(ctx, url)
}

// preflightByRange probes with "bytes=0-0": a 206 proves range support and the
// Content-Range header carries the true total size.
func (e *Engine) probeByRange(ctx context.Context, url string) (remoteInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return remoteInfo{}, err
	}
	req.Header.Set("User-Agent", e.opts.UserAgent)
	req.Header.Set("Range", "bytes=0-0")

	resp, err := e.client.Do(req)
	if err != nil {
		return remoteInfo{}, err
	}
	defer drainClose(resp.Body)

	info := remoteInfo{
		ETag:         resp.Header.Get("ETag"),
		LastModified: resp.Header.Get("Last-Modified"),
	}
	switch resp.StatusCode {
	case http.StatusPartialContent:
		info.Ranges = true
		info.Size = totalFromContentRange(resp.Header.Get("Content-Range"))
	case http.StatusOK:
		info.Size = resp.ContentLength
	default:
		return remoteInfo{}, &httpError{status: resp.StatusCode, url: url}
	}
	e.log().Info("preflight", "url", url, "method", "GET range", "status", resp.StatusCode,
		"size", info.Size, "ranges", info.Ranges, "etag", info.ETag)
	return info, nil
}

// totalFromContentRange pulls the total out of "bytes 0-0/18400314350".
func totalFromContentRange(header string) int64 {
	slash := strings.LastIndex(header, "/")
	if slash < 0 {
		return 0
	}
	total, err := strconv.ParseInt(strings.TrimSpace(header[slash+1:]), 10, 64)
	if err != nil {
		return 0
	}
	return total
}

// Run performs the transfer, resuming if a usable sidecar is present. It
// returns when the file is complete, an error is fatal, or ctx is cancelled —
// and in every case the sidecar on disk describes what has been written, so a
// cancelled run is a paused one.
func (e *Engine) Run(ctx context.Context) error {
	info, err := e.preflight(ctx)
	if err != nil {
		return err
	}
	// Size is the first integrity check: if the server disagrees with the
	// catalog, we are not looking at the file we were promised.
	if e.opts.Size > 0 && info.Size > 0 && info.Size != e.opts.Size {
		return fmt.Errorf("size mismatch: catalog says %d bytes, server says %d", e.opts.Size, info.Size)
	}
	size := info.Size
	if size <= 0 {
		size = e.opts.Size
	}

	if err := e.prepare(info, size); err != nil {
		return err
	}
	defer func() {
		e.stateMu.Lock()
		file := e.file
		e.file = nil
		e.stateMu.Unlock()
		if file != nil {
			_ = file.Sync()
			_ = file.Close()
		}
	}()

	err = e.runTransfer(ctx)
	if errors.Is(err, ErrRangesUnsupported) {
		// The server accepted our ranged request but answered with the whole
		// body: drop to a single stream and start the file again.
		if err = e.restartSingleStream(info, size); err == nil {
			err = e.runTransfer(ctx)
		}
	}
	if err != nil {
		return err
	}

	if done := e.prog.Load().Done(); size > 0 && done != size {
		return fmt.Errorf("incomplete download: %d of %d bytes", done, size)
	}
	return nil
}

// prepare loads or rebuilds the resume state and opens the partial file.
func (e *Engine) prepare(info remoteInfo, size int64) error {
	state := loadState(e.statePath)
	resuming := state.usableFor(e.primary().url, info)
	if !resuming {
		state = &State{
			URL:          e.primary().url,
			Size:         size,
			ETag:         info.ETag,
			LastModified: info.LastModified,
			Ranges:       info.Ranges,
			Chunks:       planChunks(size, e.opts.Connections),
		}
	}

	flags := os.O_CREATE | os.O_RDWR
	if !resuming {
		// Anything already in the partial file belongs to a different version
		// of the remote object; keeping it would corrupt the result.
		flags |= os.O_TRUNC
	}
	file, err := os.OpenFile(e.partPath, flags, 0o644)
	if err != nil {
		return err
	}
	if size > 0 {
		if err := file.Truncate(size); err != nil {
			file.Close()
			return err
		}
	}

	prog := newProgress(size, chunkLengths(state.Chunks), e.workerCount(state))
	var resumed int64
	for i, c := range state.Chunks {
		prog.set(i, c.Done)
		resumed += c.Done
	}
	if resuming {
		e.log().Info("resuming", "chunks", len(state.Chunks), "bytes", resumed, "of", size)
	} else {
		e.log().Info("planned", "chunks", len(state.Chunks), "connections", e.workerCount(state), "size", size)
	}

	e.stateMu.Lock()
	e.state, e.file = state, file
	e.stateMu.Unlock()
	e.prog.Store(prog)
	return nil
}

// restartSingleStream rebuilds state for a server that will not serve ranges.
func (e *Engine) restartSingleStream(info remoteInfo, size int64) error {
	e.stateMu.Lock()
	file := e.file
	e.state = &State{
		URL:          e.primary().url,
		Size:         size,
		ETag:         info.ETag,
		LastModified: info.LastModified,
		Ranges:       false,
		Chunks:       []Chunk{{Start: 0, End: size}},
	}
	state := e.state
	e.stateMu.Unlock()

	if file == nil {
		return errors.New("download: partial file is closed")
	}
	if err := file.Truncate(0); err != nil {
		return err
	}
	if size > 0 {
		if err := file.Truncate(size); err != nil {
			return err
		}
	}
	e.log().Info("server ignored ranges, falling back to a single stream")
	e.prog.Store(newProgress(size, chunkLengths(state.Chunks), 1))
	return nil
}

// chunkLengths extracts the planned size of each chunk for progress reporting.
func chunkLengths(chunks []Chunk) []int64 {
	lengths := make([]int64, len(chunks))
	for i, c := range chunks {
		lengths[i] = c.Len()
	}
	return lengths
}

// workerCount never exceeds the number of chunks there are to fetch, nor what
// the sources will tolerate: public mirrors commonly refuse a browser's worth
// of parallel connections, and being refused is slower than being polite.
func (e *Engine) workerCount(state *State) int {
	workers := e.opts.Connections
	if !state.Ranges {
		return 1
	}
	if len(state.Chunks) < workers {
		workers = len(state.Chunks)
	}
	if limit := e.connectionLimit(); limit > 0 && limit < workers {
		workers = limit
	}
	if workers < 1 {
		workers = 1
	}
	return workers
}

// connectionLimit is how many connections the current sources can take in
// total. Spare mirrors do not add capacity: they are there for failover.
func (e *Engine) connectionLimit() int {
	if e.opts.MaxPerHost <= 0 {
		return 0
	}
	hosts := 1
	if !e.opts.PreferPrimary {
		hosts = len(e.healthyMirrors())
	}
	if hosts < 1 {
		hosts = 1
	}
	return e.opts.MaxPerHost * hosts
}

// runTransfer starts the workers plus the state saver and waits for both.
func (e *Engine) runTransfer(parent context.Context) error {
	e.stateMu.Lock()
	state := e.state
	e.stateMu.Unlock()

	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	saverDone := make(chan struct{})
	go func() {
		defer close(saverDone)
		e.saveLoop(ctx)
	}()

	err := e.transfer(ctx, state, e.workerCount(state))

	cancel()
	<-saverDone

	// Whatever happened, persist what landed: this is what makes Ctrl-C safe.
	e.persist()
	return err
}

// transfer hands chunks to a fixed pool of workers. The first fatal error
// cancels the rest rather than letting them run on pointlessly.
func (e *Engine) transfer(ctx context.Context, state *State, workers int) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	prog := e.prog.Load()
	queue := make(chan int, len(state.Chunks))
	for i, c := range state.Chunks {
		if c.Len() == 0 || prog.chunkDone(i) < c.Len() {
			queue <- i
		}
	}
	close(queue)
	if len(queue) == 0 {
		return nil
	}

	var (
		wg       sync.WaitGroup
		once     sync.Once
		firstErr error
	)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for idx := range queue {
				if ctx.Err() != nil {
					return
				}
				if err := e.fetchChunk(ctx, state, idx, worker); err != nil {
					once.Do(func() {
						firstErr = err
						cancel() // stop the siblings; their progress is kept
					})
					return
				}
			}
		}(w)
	}
	wg.Wait()

	if firstErr != nil {
		return firstErr
	}
	// A cancelled context here means the caller stopped us (Ctrl-C), which is a
	// pause rather than a failure: the sidecar has already been written.
	return ctx.Err()
}

// saveLoop persists progress about once a second while the transfer runs.
func (e *Engine) saveLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			e.persist()
		}
	}
}

// persist copies the live counters into the state file.
func (e *Engine) persist() {
	prog := e.prog.Load()
	if prog == nil {
		return
	}
	e.stateMu.Lock()
	defer e.stateMu.Unlock()
	if e.state == nil {
		return
	}
	for i := range e.state.Chunks {
		e.state.Chunks[i].Done = prog.chunkDone(i)
	}
	_ = save(e.statePath, e.state)
}

// Finalize promotes the verified partial file to its final name and drops the
// resume sidecar. It is deliberately separate from Run: nothing gets the real
// file name until verification has passed.
func (e *Engine) Finalize() error {
	if err := os.Rename(e.partPath, e.opts.Dest); err != nil {
		return err
	}
	e.log().Info("finalized", "path", e.opts.Dest)
	_ = os.Remove(e.statePath)
	return nil
}

// Discard removes the partial file and its state, for a download that must not
// be resumed (a failed integrity check, or an abandoned transfer).
func (e *Engine) Discard() {
	_ = os.Remove(e.partPath)
	_ = os.Remove(e.statePath)
}

// Close releases the client's idle connections.
func (e *Engine) Close() {
	e.client.CloseIdleConnections()
}
