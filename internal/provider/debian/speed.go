package debian

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"sync"
	"time"
)

const (
	// latencyProbe is the tiny read used to see which mirrors answer quickly
	// and actually carry the file.
	latencyProbe = 16 << 10
	// speedProbe is the larger read used to rank the survivors by throughput,
	// which is what matters for a multi-gigabyte image.
	speedProbe = 256 << 10

	latencyWorkers = 16
	speedWorkers   = 4
	// speedFinalists is how many of the quickest mirrors get measured properly,
	// and therefore how many sources a download ends up spread across.
	speedFinalists = 10
)

// measurement is one mirror's result.
type measurement struct {
	site    mirrorSite
	url     string
	latency time.Duration
	rate    float64 // bytes per second
}

// rankMirrors measures mirrors against the file that is about to be downloaded:
// first a quick latency screen across all of them, then a real throughput read
// from the quickest few. Mirrors that do not carry the file drop out on the way.
func (p *Provider) rankMirrors(ctx context.Context, path string) ([]measurement, error) {
	sites := p.sites(ctx)
	if len(sites) == 0 {
		return nil, fmt.Errorf("no mirror list available")
	}

	candidates := make([]measurement, 0, len(sites))
	for _, site := range sites {
		url, ok := site.URL(path)
		if !ok {
			continue // this tree is only on Debian's own server
		}
		candidates = append(candidates, measurement{site: site, url: url})
	}

	quick := p.probe(ctx, candidates, latencyProbe, 4*time.Second, latencyWorkers)
	if len(quick) == 0 {
		return nil, fmt.Errorf("no mirror answered")
	}
	sort.Slice(quick, func(i, j int) bool { return quick[i].latency < quick[j].latency })
	p.logger.Info("mirror latency screen", "answered", len(quick), "quickest", quick[0].site.Host,
		"latency", quick[0].latency)

	if len(quick) > speedFinalists {
		quick = quick[:speedFinalists]
	}

	ranked := p.probe(ctx, quick, speedProbe, 12*time.Second, speedWorkers)
	if len(ranked) == 0 {
		return nil, fmt.Errorf("no mirror finished the speed test")
	}
	sort.Slice(ranked, func(i, j int) bool { return ranked[i].rate > ranked[j].rate })

	for _, m := range ranked {
		p.logger.Info("mirror measured", "host", m.site.Host, "country", m.site.Country,
			"latency", m.latency, "rate", fmt.Sprintf("%.1f MiB/s", m.rate/(1<<20)))
	}
	return ranked, nil
}

// probe reads the first bytes of the file from each candidate and times it.
func (p *Provider) probe(ctx context.Context, candidates []measurement, want int64, timeout time.Duration, workers int) []measurement {
	jobs := make(chan measurement)
	var results []measurement
	var mu sync.Mutex
	var wg sync.WaitGroup

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for candidate := range jobs {
				measured, err := p.timeRead(ctx, candidate, want, timeout)
				if err != nil {
					p.logger.Debug("mirror rejected", "host", candidate.site.Host, "err", err.Error())
					continue
				}
				mu.Lock()
				results = append(results, measured)
				mu.Unlock()
			}
		}()
	}

	for _, candidate := range candidates {
		select {
		case jobs <- candidate:
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return results
		}
	}
	close(jobs)
	wg.Wait()
	return results
}

// timeRead fetches a byte range and reports how long it took. A mirror that
// answers anything but 206 is not usable for a chunked download anyway.
func (p *Provider) timeRead(ctx context.Context, candidate measurement, want int64, timeout time.Duration) (measurement, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, candidate.url, nil)
	if err != nil {
		return candidate, err
	}
	req.Header.Set("User-Agent", p.agent)
	req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", want-1))

	started := time.Now()
	resp, err := p.client.Do(req)
	if err != nil {
		return candidate, err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusPartialContent {
		return candidate, fmt.Errorf("status %s", resp.Status)
	}
	firstByte := time.Since(started)

	read, err := io.Copy(io.Discard, io.LimitReader(resp.Body, want))
	elapsed := time.Since(started)
	if err != nil {
		return candidate, err
	}
	if read < want {
		return candidate, fmt.Errorf("served %d of %d bytes", read, want)
	}

	candidate.latency = firstByte
	if seconds := elapsed.Seconds(); seconds > 0 {
		candidate.rate = float64(read) / seconds
	}
	return candidate, nil
}
