package download

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"time"

	"golang.org/x/time/rate"
)

// fetchChunk downloads one byte range, retrying transient failures with
// exponential backoff. Each attempt resumes from whatever already landed, so a
// connection that dies at 90% costs only the last 10%.
func (e *Engine) fetchChunk(ctx context.Context, state *State, idx, worker int) error {
	chunk := state.Chunks[idx]
	prog := e.prog.Load()

	prog.claim(worker, idx)
	defer prog.release(worker)

	// Swapping to another source should not eat into the retry budget for the
	// chunk itself, so allow one extra attempt per alternative source.
	attempts := e.opts.MaxAttempts + len(e.mirrors) - 1

	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		done := prog.chunkDone(idx)
		if chunk.Len() > 0 && done >= chunk.Len() {
			return nil
		}

		source, err := e.pickMirror(worker, attempt)
		if err != nil {
			return fmt.Errorf("chunk %d: %w", idx, err)
		}
		prog.useMirror(worker, source.host)

		e.log().Debug("chunk started", "chunk", idx, "conn", worker+1, "host", source.host,
			"start", chunk.Start+done, "end", chunk.End, "attempt", attempt+1)

		err = e.fetchOnce(ctx, state, idx, worker, chunk, done, source)
		if err == nil {
			source.succeeded()
			e.log().Debug("chunk done", "chunk", idx, "conn", worker+1, "bytes", prog.chunkDone(idx))
			return nil
		}
		lastErr = err

		// A server that ignores ranges is not a source problem: the engine has
		// to replan the whole transfer, so hand that straight back.
		if errors.Is(err, ErrRangesUnsupported) || ctx.Err() != nil {
			return err
		}

		if !retryable(err) {
			// A 403 or 404 is final for this source, not for the download.
			// Retire it first and then look for somewhere else to go: two
			// workers can hit the same bad mirror at once, and neither should
			// conclude there is nothing left while the other is still retiring.
			e.retire(source, err.Error())
			if len(e.healthyMirrors()) > 0 {
				continue
			}
			e.log().Error("chunk failed", "chunk", idx, "conn", worker+1, "err", err.Error())
			return fmt.Errorf("chunk %d: %w", idx, err)
		}

		if source.failed() {
			e.log().Warn("mirror disabled", "host", source.host, "reason", err.Error())
			if len(e.healthyMirrors()) == 0 {
				return fmt.Errorf("chunk %d: %w", idx, err)
			}
		}
		delay := backoff(attempt)
		e.log().Warn("chunk retrying", "chunk", idx, "conn", worker+1,
			"err", err.Error(), "in", delay)

		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return fmt.Errorf("chunk %d: giving up after %d attempts: %w", idx, attempts, lastErr)
}

// backoff grows 1s, 2s, 4s… capped at 30s, with jitter so retries from several
// workers do not land on the server in lockstep.
func backoff(attempt int) time.Duration {
	delay := time.Second << attempt
	if delay > 30*time.Second {
		delay = 30 * time.Second
	}
	return delay/2 + time.Duration(rand.Int63n(int64(delay/2)+1))
}

// fetchOnce makes a single ranged request and streams it to its offset in the
// file. Bytes go straight from the socket to disk through one reusable buffer.
func (e *Engine) fetchOnce(ctx context.Context, state *State, idx, worker int, chunk Chunk, done int64, source *mirror) error {
	prog := e.prog.Load()
	if !state.Ranges && done > 0 {
		// Without range support there is nothing to resume from: start over.
		prog.set(idx, 0)
		done = 0
	}
	start := chunk.Start + done

	reqCtx, cancelReq := context.WithCancel(ctx)
	defer cancelReq()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, source.url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", e.opts.UserAgent)

	wantRange := state.Ranges && chunk.Len() > 0
	if wantRange {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, chunk.End-1))
		// If-Range only makes sense against the mirror whose ETag we recorded;
		// other mirrors compute their own, and a mismatch there would restart
		// the chunk for no reason. Integrity still comes from the final hash.
		if state.ETag != "" && source.etag == state.ETag {
			// If the object changed under us, the server answers 200 with the
			// whole file instead of the range we asked for, which we detect below.
			req.Header.Set("If-Range", state.ETag)
		}
	}

	resp, err := e.client.Do(req)
	if err != nil {
		return stallAware(ctx, reqCtx, e.opts.StallTimeout, err)
	}
	defer drainClose(resp.Body)

	switch {
	case wantRange && resp.StatusCode == http.StatusOK:
		return ErrRangesUnsupported
	case wantRange && resp.StatusCode != http.StatusPartialContent:
		return &httpError{status: resp.StatusCode, url: source.url}
	case !wantRange && resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent:
		return &httpError{status: resp.StatusCode, url: source.url}
	}

	// A connection that goes quiet is worse than one that fails: cancel the
	// request if no bytes arrive for StallTimeout, then let the retry loop run.
	watchdog := time.AfterFunc(e.opts.StallTimeout, cancelReq)
	defer watchdog.Stop()

	var src io.Reader = &watchReader{r: resp.Body, timer: watchdog, timeout: e.opts.StallTimeout}
	if e.limiter != nil {
		src = &limitedReader{r: src, limiter: e.limiter, ctx: reqCtx}
	}
	remaining := chunk.Len() - done
	if remaining > 0 {
		// Never write past the chunk: a server sending extra bytes must not be
		// allowed to scribble over the next worker's range.
		src = io.LimitReader(src, remaining)
	}

	dst := &countingWriter{
		w:      io.NewOffsetWriter(e.file, start),
		prog:   prog,
		chunk:  idx,
		worker: worker,
	}

	buf := e.bufs.Get().(*[]byte)
	defer e.bufs.Put(buf)

	written, err := io.CopyBuffer(dst, src, *buf)
	if err != nil {
		return stallAware(ctx, reqCtx, e.opts.StallTimeout, err)
	}
	if remaining > 0 && written != remaining {
		return fmt.Errorf("short read: got %d of %d bytes", written, remaining)
	}
	return nil
}

// stallAware turns "our watchdog cancelled the request" into a retryable stall
// error, while leaving a genuine caller cancellation alone.
func stallAware(parent, req context.Context, timeout time.Duration, err error) error {
	if parent.Err() == nil && req.Err() != nil {
		return fmt.Errorf("connection stalled: no data for %s", timeout)
	}
	return err
}

// watchReader resets the stall watchdog whenever bytes actually arrive.
type watchReader struct {
	r       io.Reader
	timer   *time.Timer
	timeout time.Duration
}

func (w *watchReader) Read(p []byte) (int, error) {
	n, err := w.r.Read(p)
	if n > 0 {
		w.timer.Reset(w.timeout)
	}
	return n, err
}

// limitedReader applies a shared token bucket across all workers so --limit-rate
// caps the whole transfer, not each connection.
type limitedReader struct {
	r       io.Reader
	limiter *rate.Limiter
	ctx     context.Context
}

func (l *limitedReader) Read(p []byte) (int, error) {
	n, err := l.r.Read(p)
	if n > 0 {
		if waitErr := l.limiter.WaitN(l.ctx, n); waitErr != nil && err == nil {
			return n, waitErr
		}
	}
	return n, err
}

// countingWriter records every byte that reaches the disk, per chunk and per
// worker, using atomics rather than channel sends.
type countingWriter struct {
	w      io.Writer
	prog   *Progress
	chunk  int
	worker int
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.prog.add(c.chunk, c.worker, int64(n))
	return n, err
}
