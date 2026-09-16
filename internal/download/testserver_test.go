package download

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// patternReader is a virtual file of arbitrary size whose bytes are generated
// on demand. Tests can therefore serve hundreds of megabytes without holding
// any of it in memory, which matters when the thing under test is memory use.
type patternReader struct {
	size int64
	off  int64
}

func patternByte(offset int64) byte { return byte((offset*31 + 7) % 251) }

func (p *patternReader) Read(b []byte) (int, error) {
	if p.off >= p.size {
		return 0, io.EOF
	}
	n := int64(len(b))
	if remaining := p.size - p.off; n > remaining {
		n = remaining
	}
	for i := int64(0); i < n; i++ {
		b[i] = patternByte(p.off + i)
	}
	p.off += n
	return int(n), nil
}

func (p *patternReader) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		p.off = offset
	case io.SeekCurrent:
		p.off += offset
	case io.SeekEnd:
		p.off = p.size + offset
	}
	return p.off, nil
}

// serverOpts shapes how the test server misbehaves.
type serverOpts struct {
	ignoreRanges bool // advertise ranges, then answer 200 with the whole body
	failFirst    int  // reject this many GETs with 503 before serving
	forbidGets   bool // answer 403 to ranged reads, as some mirrors do
	noETag       bool
}

type testServer struct {
	*httptest.Server
	size     int64
	served   atomic.Int64
	requests atomic.Int64
	failures atomic.Int64
}

// countingWriter counts what actually goes over the wire, which is how the
// resume test proves it did not re-download the whole file.
type countingResponseWriter struct {
	http.ResponseWriter
	counter *atomic.Int64
}

func (w *countingResponseWriter) Write(b []byte) (int, error) {
	n, err := w.ResponseWriter.Write(b)
	w.counter.Add(int64(n))
	return n, err
}

func newTestServer(t *testing.T, size int64, opts serverOpts) *testServer {
	t.Helper()
	srv := &testServer{size: size}
	modTime := time.Date(2026, 9, 10, 15, 19, 4, 0, time.UTC)

	srv.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		srv.requests.Add(1)
		if !opts.noETag {
			w.Header().Set("ETag", `"test-etag"`)
		}
		if opts.forbidGets && r.Method == http.MethodGet && r.Header.Get("Range") != "" {
			srv.failures.Add(1)
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if r.Method == http.MethodGet && opts.failFirst > 0 {
			if int(srv.failures.Add(1)) <= opts.failFirst {
				http.Error(w, "try again", http.StatusServiceUnavailable)
				return
			}
		}
		if opts.ignoreRanges {
			r.Header.Del("Range")
		}
		http.ServeContent(&countingResponseWriter{ResponseWriter: w, counter: &srv.served},
			r, "file.bin", modTime, &patternReader{size: size})
	}))
	t.Cleanup(srv.Close)
	return srv
}
