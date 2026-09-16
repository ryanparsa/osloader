package macos

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// distInfo is what a .dist file tells us that the catalog does not: the
// marketing title, the version and the build.
type distInfo struct {
	Title   string
	Version string
	Build   string
}

var (
	reBuild   = regexp.MustCompile(`<key>BUILD</key>\s*<string>([^<]+)</string>`)
	reVersion = regexp.MustCompile(`<key>VERSION</key>\s*<string>([^<]+)</string>`)
	reVersStr = regexp.MustCompile(`versStr="([^"]+)"`)
	reTitle   = regexp.MustCompile(`<title>([^<]*)</title>`)
)

// parseDist walks the distribution XML for <title> and the auxinfo key/string
// pairs. The file also carries a large CDATA script block, which the tokenizer
// steps over safely; a regex pass covers products whose auxinfo is missing.
func parseDist(raw []byte) distInfo {
	var info distInfo

	dec := xml.NewDecoder(strings.NewReader(string(raw)))
	dec.Strict = false
	dec.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) { return r, nil }

	var inAuxinfo, inTitle, inKey, inString bool
	var key, value string

	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "auxinfo":
				inAuxinfo = true
			case "title":
				inTitle = true
			case "key":
				inKey = inAuxinfo
			case "string":
				inString = inAuxinfo
			}
		case xml.CharData:
			switch {
			case inTitle && info.Title == "":
				info.Title = strings.TrimSpace(string(t))
			case inKey:
				key = strings.TrimSpace(string(t))
			case inString:
				value = strings.TrimSpace(string(t))
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "auxinfo":
				inAuxinfo = false
			case "title":
				inTitle = false
			case "key":
				inKey = false
			case "string":
				if inString {
					switch key {
					case "VERSION":
						info.Version = value
					case "BUILD":
						info.Build = value
					}
					key, value = "", ""
				}
				inString = false
			}
		}
	}

	text := string(raw)
	if info.Build == "" {
		if m := reBuild.FindStringSubmatch(text); m != nil {
			info.Build = m[1]
		}
	}
	if info.Version == "" {
		if m := reVersion.FindStringSubmatch(text); m != nil {
			info.Version = m[1]
		} else if m := reVersStr.FindStringSubmatch(text); m != nil {
			info.Version = m[1]
		}
	}
	if info.Title == "" {
		if m := reTitle.FindStringSubmatch(text); m != nil {
			info.Title = strings.TrimSpace(m[1])
		}
	}
	return info
}

// fetchDist returns the parsed .dist for a product, using a small on-disk cache
// because these files never change once published.
func fetchDist(ctx context.Context, client *http.Client, productID, url string) (distInfo, error) {
	if url == "" {
		return distInfo{}, fmt.Errorf("product %s has no distribution file", productID)
	}
	if raw, err := os.ReadFile(distCachePath(productID)); err == nil && len(raw) > 0 {
		logger.Debug("dist cached", "product", productID)
		return parseDist(raw), nil
	}
	logger.Debug("dist fetching", "product", productID, "url", url)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return distInfo{}, err
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := client.Do(req)
	if err != nil {
		return distInfo{}, err
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return distInfo{}, fmt.Errorf("dist %s: unexpected status %s", url, resp.Status)
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return distInfo{}, err
	}
	writeDistCache(productID, raw)
	return parseDist(raw), nil
}

func distCachePath(productID string) string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "osloader", "dist", productID+".dist")
}

// writeDistCache is best effort: a cold cache only costs a refetch.
func writeDistCache(productID string, raw []byte) {
	path := distCachePath(productID)
	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".dist-*")
	if err != nil {
		return
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return
	}
	if err := tmp.Close(); err != nil {
		return
	}
	_ = os.Rename(tmp.Name(), path)
}

// fetchDists resolves many products' .dist files with a bounded worker pool —
// dozens of small requests, but never dozens at once.
func fetchDists(ctx context.Context, client *http.Client, products map[string]product, workers int) map[string]distInfo {
	type job struct{ id, url string }

	jobs := make(chan job)
	out := make(map[string]distInfo, len(products))
	var mu sync.Mutex
	var wg sync.WaitGroup

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				info, err := fetchDist(ctx, client, j.id, j.url)
				if err != nil {
					continue
				}
				mu.Lock()
				out[j.id] = info
				mu.Unlock()
			}
		}()
	}

	for id, p := range products {
		select {
		case jobs <- job{id: id, url: p.distURL()}:
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return out
		}
	}
	close(jobs)
	wg.Wait()
	return out
}
