package download

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ryanparsa/osloader/internal/logging"
)

// checkPattern verifies the downloaded file byte for byte against the virtual
// file the server generated, streaming so the check itself stays cheap.
func checkPattern(t *testing.T, path string, size int64) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != size {
		t.Fatalf("file is %d bytes, want %d", info.Size(), size)
	}

	buf := make([]byte, 64<<10)
	var offset int64
	for {
		n, err := f.Read(buf)
		for i := 0; i < n; i++ {
			if buf[i] != patternByte(offset+int64(i)) {
				t.Fatalf("byte %d differs: got %d, want %d", offset+int64(i), buf[i], patternByte(offset+int64(i)))
			}
		}
		offset += int64(n)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
}

func newTestEngine(t *testing.T, url, dest string, size int64, connections int) *Engine {
	t.Helper()
	engine, err := New(Options{
		URLs:         []string{url},
		Dest:         dest,
		Size:         size,
		Connections:  connections,
		StallTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(engine.Close)
	return engine
}

// TestParallelDownloadMatchesSource is the base case: several connections, one
// correct file.
func TestParallelDownloadMatchesSource(t *testing.T) {
	const size = 32 << 20
	srv := newTestServer(t, size, serverOpts{})
	dest := filepath.Join(t.TempDir(), "file.bin")

	engine := newTestEngine(t, srv.URL, dest, size, 4)
	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if err := engine.Finalize(); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	checkPattern(t, dest, size)
	if _, err := os.Stat(engine.StatePath()); !os.IsNotExist(err) {
		t.Error("the resume sidecar should be gone once the file is final")
	}
	if got := engine.Snapshot().Done; got != size {
		t.Errorf("progress reported %d bytes, want %d", got, size)
	}
}

// TestResumeAfterCancel is the whole point of the sidecar: an interrupted
// transfer must continue rather than start over.
func TestResumeAfterCancel(t *testing.T) {
	const size = 48 << 20
	srv := newTestServer(t, size, serverOpts{})
	dest := filepath.Join(t.TempDir(), "file.bin")

	ctx, cancel := context.WithCancel(context.Background())
	first := newTestEngine(t, srv.URL, dest, size, 4)

	// Cancel as soon as a meaningful amount has landed.
	go func() {
		defer cancel()
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if first.Snapshot().Done > size/8 {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()

	err := first.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("interrupted run returned %v, want context.Canceled", err)
	}

	state := loadState(first.StatePath())
	if state == nil {
		t.Fatal("no sidecar written for an interrupted download")
	}
	var saved int64
	for _, c := range state.Chunks {
		saved += c.Done
	}
	if saved == 0 {
		t.Fatal("sidecar recorded no progress")
	}
	servedFirst := srv.served.Load()

	second := newTestEngine(t, srv.URL, dest, size, 4)
	if err := second.Run(context.Background()); err != nil {
		t.Fatalf("resumed run: %v", err)
	}
	if err := second.Finalize(); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	checkPattern(t, dest, size)

	servedSecond := srv.served.Load() - servedFirst
	if servedSecond >= size {
		t.Errorf("resume re-fetched %d bytes of a %d byte file — it restarted instead of resuming",
			servedSecond, int64(size))
	}
}

// TestRestartsWhenRemoteChanged guards against splicing together two different
// versions of a file: a moved ETag must invalidate saved progress.
func TestRestartsWhenRemoteChanged(t *testing.T) {
	const size = 16 << 20
	srv := newTestServer(t, size, serverOpts{})
	dir := t.TempDir()
	dest := filepath.Join(dir, "file.bin")

	stale := &State{
		URL:    srv.URL,
		Size:   size,
		ETag:   `"a-different-object"`,
		Ranges: true,
		Chunks: []Chunk{{Start: 0, End: size, Done: size / 2}},
	}
	if err := save(dest+".part.json", stale); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest+".part", make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}

	engine := newTestEngine(t, srv.URL, dest, size, 4)
	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if err := engine.Finalize(); err != nil {
		t.Fatal(err)
	}
	// If the stale "done" bytes had been trusted, half the file would be zeros.
	checkPattern(t, dest, size)
}

// TestFallsBackWhenRangesIgnored covers a server that advertises range support
// and then sends the whole body anyway.
func TestFallsBackWhenRangesIgnored(t *testing.T) {
	const size = 24 << 20
	srv := newTestServer(t, size, serverOpts{ignoreRanges: true})
	dest := filepath.Join(t.TempDir(), "file.bin")

	engine := newTestEngine(t, srv.URL, dest, size, 4)
	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if err := engine.Finalize(); err != nil {
		t.Fatal(err)
	}
	checkPattern(t, dest, size)
}

// TestRetriesTransientFailures: a 503 is not a reason to give up.
func TestRetriesTransientFailures(t *testing.T) {
	const size = 12 << 20
	srv := newTestServer(t, size, serverOpts{failFirst: 2})
	dest := filepath.Join(t.TempDir(), "file.bin")

	engine := newTestEngine(t, srv.URL, dest, size, 2)
	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if err := engine.Finalize(); err != nil {
		t.Fatal(err)
	}
	checkPattern(t, dest, size)
	if srv.failures.Load() < 2 {
		t.Error("the test server never injected its failures")
	}
}

// TestRejectsSizeMismatch stops a download whose size contradicts the catalog
// before a single byte is written.
func TestRejectsSizeMismatch(t *testing.T) {
	const size = 4 << 20
	srv := newTestServer(t, size, serverOpts{})
	dest := filepath.Join(t.TempDir(), "file.bin")

	engine := newTestEngine(t, srv.URL, dest, size+1, 2)
	err := engine.Run(context.Background())
	if err == nil {
		t.Fatal("expected a size mismatch error")
	}
	if _, statErr := os.Stat(engine.PartPath()); statErr == nil {
		t.Error("nothing should have been written for a mismatched size")
	}
}

// TestMemoryStaysBounded is the no-leak requirement in test form: the heap must
// not track the file size. The file here is far larger than the ceiling.
func TestMemoryStaysBounded(t *testing.T) {
	if testing.Short() {
		t.Skip("allocates a 256 MiB file on disk")
	}
	const size = 256 << 20
	const ceiling = 64 << 20

	srv := newTestServer(t, size, serverOpts{})
	dest := filepath.Join(t.TempDir(), "file.bin")
	engine := newTestEngine(t, srv.URL, dest, size, 8)

	var peak atomic.Uint64
	stop := make(chan struct{})
	sampling := make(chan struct{})
	go func() {
		defer close(sampling)
		var stats runtime.MemStats
		for {
			select {
			case <-stop:
				return
			case <-time.After(20 * time.Millisecond):
				runtime.ReadMemStats(&stats)
				if stats.HeapAlloc > peak.Load() {
					peak.Store(stats.HeapAlloc)
				}
			}
		}
	}()

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	close(stop)
	<-sampling

	if got := peak.Load(); got > ceiling {
		t.Errorf("peak heap %d bytes while downloading %d — memory is tracking file size", got, int64(size))
	}
}

// TestCheckURL keeps downloads on the vendor's own hosts.
func TestCheckURL(t *testing.T) {
	allowed := []string{"swcdn.apple.com", ".apple.com"}
	cases := []struct {
		url string
		ok  bool
	}{
		{"https://swcdn.apple.com/content/InstallAssistant.pkg", true},
		{"https://updates.apple.com/file.pkg", true},
		{"http://swcdn.apple.com/file.pkg", false},
		{"https://swcdn.apple.com.evil.test/file.pkg", false},
		{"https://example.com/file.pkg", false},
	}
	for _, tc := range cases {
		err := checkURL(tc.url, allowed)
		if tc.ok && err != nil {
			t.Errorf("%s was rejected: %v", tc.url, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("%s should have been rejected", tc.url)
		}
	}
	if err := checkURL("http://127.0.0.1:1234/file", nil); err != nil {
		t.Errorf("an empty allowlist should permit test servers: %v", err)
	}
}

// TestPerConnectionProgress: the UI needs to see which chunk each connection
// holds and how much of it has landed, and idle connections must report as idle.
func TestPerConnectionProgress(t *testing.T) {
	const size = 32 << 20
	srv := newTestServer(t, size, serverOpts{})
	dest := filepath.Join(t.TempDir(), "file.bin")
	engine := newTestEngine(t, srv.URL, dest, size, 4)

	var sawActive atomic.Bool
	stop := make(chan struct{})
	sampling := make(chan struct{})
	go func() {
		defer close(sampling)
		for {
			select {
			case <-stop:
				return
			case <-time.After(2 * time.Millisecond):
				for _, w := range engine.Snapshot().Workers {
					if w.Active() && w.ChunkTotal > 0 {
						sawActive.Store(true)
					}
				}
			}
		}
	}()

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	close(stop)
	<-sampling

	if !sawActive.Load() {
		t.Error("no connection ever reported the chunk it was working on")
	}

	snap := engine.Snapshot()
	if len(snap.Workers) != 4 {
		t.Fatalf("snapshot has %d connections, want 4", len(snap.Workers))
	}
	var total int64
	for i, w := range snap.Workers {
		if w.Bytes == 0 {
			t.Errorf("connection %d moved no bytes", i+1)
		}
		if w.Active() {
			t.Errorf("connection %d still claims chunk %d after the run finished", i+1, w.Chunk)
		}
		total += w.Bytes
	}
	if total != size {
		t.Errorf("connections account for %d bytes, want %d", total, int64(size))
	}
}

// TestVerboseLogRecordsRetries: --verbose has to explain a slow download, so a
// retry must show up in the log with its cause.
func TestVerboseLogRecordsRetries(t *testing.T) {
	const size = 12 << 20
	srv := newTestServer(t, size, serverOpts{failFirst: 1})
	dest := filepath.Join(t.TempDir(), "file.bin")

	var buf bytes.Buffer
	engine, err := New(Options{
		URLs: []string{srv.URL}, Dest: dest, Size: size, Connections: 2,
		StallTimeout: 10 * time.Second,
		Logger:       logging.New(&buf, true),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	log := buf.String()
	for _, want := range []string{"preflight", "planned", "chunk started", "chunk retrying", "503"} {
		if !strings.Contains(log, want) {
			t.Errorf("verbose log does not mention %q:\n%s", want, log)
		}
	}
}

// TestMirrorsShareTheWork: several sources for one file should all be used.
func TestMirrorsShareTheWork(t *testing.T) {
	const size = 32 << 20
	first := newTestServer(t, size, serverOpts{})
	second := newTestServer(t, size, serverOpts{})
	dest := filepath.Join(t.TempDir(), "file.bin")

	engine, err := New(Options{
		URLs: []string{first.URL, second.URL}, Dest: dest, Size: size,
		Connections: 4, StallTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if err := engine.Finalize(); err != nil {
		t.Fatal(err)
	}
	checkPattern(t, dest, size)

	if second.served.Load() == 0 {
		t.Error("the second mirror was never used")
	}
	if first.served.Load()+second.served.Load() < int64(size) {
		t.Errorf("mirrors served %d bytes for a %d byte file",
			first.served.Load()+second.served.Load(), int64(size))
	}
}

// TestMirrorFailoverCompletes: a mirror that keeps failing is retired and the
// download finishes on the healthy one.
func TestMirrorFailoverCompletes(t *testing.T) {
	const size = 48 << 20 // six chunks, so the bad mirror is picked repeatedly
	good := newTestServer(t, size, serverOpts{})
	bad := newTestServer(t, size, serverOpts{failFirst: 1000}) // never recovers
	dest := filepath.Join(t.TempDir(), "file.bin")

	var log bytes.Buffer
	engine, err := New(Options{
		URLs: []string{good.URL, bad.URL}, Dest: dest, Size: size,
		Connections: 6, StallTimeout: 10 * time.Second,
		Logger: logging.New(&log, true),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if err := engine.Finalize(); err != nil {
		t.Fatal(err)
	}
	checkPattern(t, dest, size)

	if !strings.Contains(log.String(), "mirror disabled") {
		t.Errorf("the failing mirror was never retired:\n%s", log.String())
	}
}

// TestMirrorWithDifferentSizeIsRejected: joining chunks from two different
// files would silently corrupt the result, so a mismatched mirror is dropped
// before any of its bytes are used.
func TestMirrorWithDifferentSizeIsRejected(t *testing.T) {
	const size = 16 << 20
	good := newTestServer(t, size, serverOpts{})
	other := newTestServer(t, size/2, serverOpts{})
	dest := filepath.Join(t.TempDir(), "file.bin")

	var log bytes.Buffer
	engine, err := New(Options{
		URLs: []string{good.URL, other.URL}, Dest: dest, Size: size,
		Connections: 4, StallTimeout: 10 * time.Second,
		Logger: logging.New(&log, true),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if err := engine.Finalize(); err != nil {
		t.Fatal(err)
	}
	checkPattern(t, dest, size)

	if other.served.Load() > 1024 {
		t.Errorf("a mirror serving a different file contributed %d bytes", other.served.Load())
	}
	if !strings.Contains(log.String(), "mirror disabled") {
		t.Errorf("the mismatched mirror was not reported:\n%s", log.String())
	}
}

// TestMirrorRefusingWithForbiddenFailsOver is the regression test for a real
// failure: one mirror answered 403 to ranged reads and the whole download died
// with it. A definitive error is final for that source, not for the transfer.
func TestMirrorRefusingWithForbiddenFailsOver(t *testing.T) {
	const size = 24 << 20
	refusing := newTestServer(t, size, serverOpts{forbidGets: true})
	good := newTestServer(t, size, serverOpts{})
	dest := filepath.Join(t.TempDir(), "file.bin")

	var log bytes.Buffer
	engine, err := New(Options{
		URLs: []string{refusing.URL, good.URL}, Dest: dest, Size: size,
		Connections: 4, StallTimeout: 10 * time.Second,
		Logger: logging.New(&log, true),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if err := engine.Finalize(); err != nil {
		t.Fatal(err)
	}
	checkPattern(t, dest, size)

	if refusing.failures.Load() == 0 {
		t.Error("the refusing mirror was never tried")
	}
	if !strings.Contains(log.String(), "mirror disabled") {
		t.Errorf("the refusing mirror was not retired:\n%s", log.String())
	}
}

// TestForbiddenWithNoAlternativeStillFails: with a single source, a 403 is the
// end of the road and must be reported rather than retried forever.
func TestForbiddenWithNoAlternativeStillFails(t *testing.T) {
	const size = 8 << 20
	refusing := newTestServer(t, size, serverOpts{forbidGets: true})
	dest := filepath.Join(t.TempDir(), "file.bin")

	engine := newTestEngine(t, refusing.URL, dest, size, 2)
	err := engine.Run(context.Background())
	if err == nil {
		t.Fatal("expected the download to fail")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("error = %v, want it to name the status", err)
	}
}

// TestConnectionsRespectPerHostLimit: volunteer mirrors are not CDNs, and
// several answer 403 when a client opens eight connections at once.
func TestConnectionsRespectPerHostLimit(t *testing.T) {
	const size = 64 << 20
	srv := newTestServer(t, size, serverOpts{})
	dest := filepath.Join(t.TempDir(), "file.bin")

	engine, err := New(Options{
		URLs: []string{srv.URL}, Dest: dest, Size: size,
		Connections: 8, MaxPerHost: 3, PreferPrimary: true,
		StallTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if err := engine.Finalize(); err != nil {
		t.Fatal(err)
	}
	checkPattern(t, dest, size)

	active := 0
	for _, w := range engine.Snapshot().Workers {
		if w.Bytes > 0 {
			active++
		}
	}
	if active > 3 {
		t.Errorf("%d connections carried data, want at most the per-host limit of 3", active)
	}
}

// TestPreferPrimaryKeepsSparesInReserve: "fastest mirror" means use that one,
// with the others there only if it fails.
func TestPreferPrimaryKeepsSparesInReserve(t *testing.T) {
	const size = 32 << 20
	fastest := newTestServer(t, size, serverOpts{})
	spare := newTestServer(t, size, serverOpts{})
	dest := filepath.Join(t.TempDir(), "file.bin")

	engine, err := New(Options{
		URLs: []string{fastest.URL, spare.URL}, Dest: dest, Size: size,
		Connections: 4, MaxPerHost: 4, PreferPrimary: true,
		StallTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()

	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	checkPattern(t, engine.PartPath(), size)

	if spare.served.Load() > 0 {
		t.Errorf("the spare mirror served %d bytes while the chosen one was healthy", spare.served.Load())
	}
	if fastest.served.Load() < int64(size) {
		t.Errorf("the chosen mirror served %d of %d bytes", fastest.served.Load(), int64(size))
	}
}
