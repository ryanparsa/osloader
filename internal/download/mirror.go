package download

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sync/atomic"
)

// mirrorFailureLimit is how many consecutive failures retire a mirror. One bad
// response is noise; three in a row means the mirror is not worth a worker.
const mirrorFailureLimit = 3

// mirror is one source for the same bytes. Several mirrors of one file are the
// normal case for Linux distributions; Apple serves a single CDN.
type mirror struct {
	url  string
	host string
	etag string

	failures atomic.Int32
	disabled atomic.Bool
}

func newMirror(raw string) (*mirror, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	// Host, not Hostname: two mirrors on the same machine (test servers, or a
	// CDN reachable on several ports) must still be distinguishable.
	return &mirror{url: raw, host: parsed.Host}, nil
}

// healthy reports whether the mirror is still in rotation.
func (m *mirror) healthy() bool { return !m.disabled.Load() }

// succeeded clears the failure streak: mirrors are retired for being
// persistently bad, not for one bad moment.
func (m *mirror) succeeded() { m.failures.Store(0) }

// failed counts a failure and reports whether that retired the mirror.
func (m *mirror) failed() bool {
	if m.failures.Add(1) >= mirrorFailureLimit && !m.disabled.Swap(true) {
		return true
	}
	return false
}

// primary is the mirror the resume state and pre-flight are keyed on.
func (e *Engine) primary() *mirror { return e.mirrors[0] }

// healthyMirrors returns the mirrors still worth using.
func (e *Engine) healthyMirrors() []*mirror {
	healthy := make([]*mirror, 0, len(e.mirrors))
	for _, m := range e.mirrors {
		if m.healthy() {
			healthy = append(healthy, m)
		}
	}
	return healthy
}

// pickMirror chooses where an attempt should go. With PreferPrimary the first
// healthy source gets the work and the others are spares — that is what
// "fastest mirror" means — and every retry still moves to a different source so
// one sick host cannot stall a chunk.
func (e *Engine) pickMirror(worker, attempt int) (*mirror, error) {
	healthy := e.healthyMirrors()
	if len(healthy) == 0 {
		return nil, errors.New("every source failed")
	}
	if e.opts.PreferPrimary {
		return healthy[attempt%len(healthy)], nil
	}
	return healthy[(worker+attempt)%len(healthy)], nil
}

// checkMirrors probes the alternates and keeps only those serving the same
// file: a mirror with a different size is a different file, and joining chunks
// from both would produce a corrupt download.
func (e *Engine) checkMirrors(ctx context.Context, info remoteInfo) {
	for _, m := range e.mirrors[1:] {
		probed, err := e.probe(ctx, m.url)
		switch {
		case err != nil:
			e.retire(m, err.Error())
		case info.Size > 0 && probed.Size > 0 && probed.Size != info.Size:
			e.retire(m, fmt.Sprintf("serves %d bytes, primary has %d", probed.Size, info.Size))
		case info.Ranges && !probed.Ranges:
			e.retire(m, "does not support range requests")
		default:
			m.etag = probed.ETag
			e.log().Info("mirror ready", "host", m.host, "size", probed.Size)
		}
	}
}

// retire takes a mirror out of rotation, with the reason in the log.
func (e *Engine) retire(m *mirror, reason string) {
	if !m.disabled.Swap(true) {
		e.log().Warn("mirror disabled", "host", m.host, "reason", reason)
	}
}
