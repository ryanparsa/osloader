package download

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ErrRangesUnsupported means the server ignored our Range request, so the
// transfer has to fall back to a single stream.
var ErrRangesUnsupported = errors.New("server does not honour range requests")

// httpError carries the status code so retry decisions can see it.
type httpError struct {
	status int
	url    string
}

func (e *httpError) Error() string { return fmt.Sprintf("%s: unexpected status %d", e.url, e.status) }

// retryable reports whether another attempt could plausibly succeed. A 403 or
// 404 is a definitive answer; a 500 or a dropped connection is not.
func retryable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if errors.Is(err, ErrRangesUnsupported) {
		return false
	}
	var he *httpError
	if errors.As(err, &he) {
		switch he.status {
		case http.StatusRequestTimeout, http.StatusTooManyRequests:
			return true
		default:
			return he.status >= 500
		}
	}
	// Network-level failures (reset, EOF mid-body, timeouts) are worth retrying.
	return true
}

// newClient builds a client sized for the number of parallel connections. There
// is deliberately no Client.Timeout: a 17 GiB transfer would trip it. Stalls are
// caught by the per-read watchdog instead.
func newClient(connections int, allowedHosts []string) *http.Client {
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 45 * time.Second,
		ExpectContinueTimeout: time.Second,
		MaxIdleConns:          connections * 2,
		MaxIdleConnsPerHost:   connections,
		IdleConnTimeout:       90 * time.Second,
		ForceAttemptHTTP2:     true,
	}
	return &http.Client{
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("too many redirects")
			}
			// Re-check every hop: a redirect must not walk us off the vendor.
			return checkURL(req.URL.String(), allowedHosts)
		},
	}
}

// checkURL enforces HTTPS and the host allowlist. An empty allowlist means the
// caller explicitly accepts any host (used by tests against httptest).
func checkURL(raw string, allowedHosts []string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if len(allowedHosts) == 0 {
		return nil
	}
	if u.Scheme != "https" {
		return fmt.Errorf("refusing non-HTTPS URL %s", raw)
	}
	host := strings.ToLower(u.Hostname())
	for _, allowed := range allowedHosts {
		allowed = strings.ToLower(allowed)
		if host == allowed || (strings.HasPrefix(allowed, ".") && strings.HasSuffix(host, allowed)) {
			return nil
		}
	}
	return fmt.Errorf("refusing host %q: not in the allowed list %v", host, allowedHosts)
}

// drainClose returns the connection to the pool instead of leaking it, without
// reading an unbounded amount of a body we no longer want.
func drainClose(body io.ReadCloser) {
	_, _ = io.Copy(io.Discard, io.LimitReader(body, 64<<10))
	_ = body.Close()
}
