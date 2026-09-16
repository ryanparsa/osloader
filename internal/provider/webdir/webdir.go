// Package webdir reads the two files a distribution's download directory
// usually offers: a checksum list and an Apache-style index. Debian and Ubuntu
// both publish exactly this, so both read it through here.
package webdir

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Checksums maps a file name to its published digest.
type Checksums map[string]string

// ParseChecksums reads "<hash>  <filename>" lines. Some projects mark binary
// files with a leading asterisk, which is not part of the name.
func ParseChecksums(r io.Reader) Checksums {
	sums := Checksums{}
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 || !IsHexDigest(fields[0]) {
			continue
		}
		sums[strings.TrimPrefix(fields[1], "*")] = strings.ToLower(fields[0])
	}
	return sums
}

// IsHexDigest keeps stray text out of the checksum table: a SHA-256 is 64 hex
// characters and nothing else.
func IsHexDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, r := range value {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f', r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}

// File is what a directory index tells us beyond the checksum.
type File struct {
	Name     string
	Size     int64
	Modified time.Time
}

// indexRow matches one row of the Apache autoindex both projects serve.
var indexRow = regexp.MustCompile(
	`<a href="([^"]+)">[^<]*</a></td><td[^>]*class="indexcollastmod">\s*([^<]*?)\s*</td><td[^>]*class="indexcolsize">\s*([^<]*?)\s*</td>`)

// alignedRow matches the older Apache table layout Ubuntu serves, where the
// date and size sit in right-aligned cells rather than named ones.
var alignedRow = regexp.MustCompile(
	`<a href="([^"]+)">[^<]*</a></td><td align="right">\s*(\d{4}-\d{2}-\d{2} \d{2}:\d{2})\s*</td><td align="right">\s*([\d.]+[KMGT]?|-)\s*</td>`)

// simpleRow matches listings where the date and size follow the link as plain
// text rather than in table cells.
var simpleRow = regexp.MustCompile(
	`<a href="([^"]+)">[^<]*</a>\s*(\d{2}-\w{3}-\d{4} \d{2}:\d{2})\s+([\d.]+[KMGT]?)`)

// ParseIndex pulls names, sizes and dates out of a directory listing, keeping
// only the files with the given suffix. The sizes there are rounded ("756M"),
// so they are for display: the digest is what a download is checked against.
func ParseIndex(body, suffix string) map[string]File {
	files := map[string]File{}

	add := func(name, when, size string, layout string) {
		if !strings.HasSuffix(name, suffix) {
			return
		}
		file := File{Name: name, Size: ParseHumanSize(size)}
		if parsed, err := time.Parse(layout, strings.TrimSpace(when)); err == nil {
			file.Modified = parsed
		}
		files[name] = file
	}

	for _, row := range indexRow.FindAllStringSubmatch(body, -1) {
		add(row[1], row[2], row[3], "2006-01-02 15:04")
	}
	for _, row := range alignedRow.FindAllStringSubmatch(body, -1) {
		add(row[1], row[2], row[3], "2006-01-02 15:04")
	}
	for _, row := range simpleRow.FindAllStringSubmatch(body, -1) {
		add(row[1], row[2], row[3], "02-Jan-2006 15:04")
	}
	return files
}

// ParseHumanSize turns an index's "756M" or "4.4G" into bytes.
func ParseHumanSize(value string) int64 {
	value = strings.TrimSpace(value)
	if value == "" || value == "-" {
		return 0
	}
	multiplier := int64(1)
	switch value[len(value)-1] {
	case 'K':
		multiplier = 1 << 10
	case 'M':
		multiplier = 1 << 20
	case 'G':
		multiplier = 1 << 30
	case 'T':
		multiplier = 1 << 40
	}
	if multiplier > 1 {
		value = value[:len(value)-1]
	}
	number, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0
	}
	return int64(number * float64(multiplier))
}

// Links returns the hrefs in a listing that end with the given suffix, for the
// indexes that carry no size or date columns.
func Links(body, suffix string) []string {
	var found []string
	seen := map[string]struct{}{}
	for _, row := range regexp.MustCompile(`<a href="([^"]+)"`).FindAllStringSubmatch(body, -1) {
		name := row[1]
		if !strings.HasSuffix(name, suffix) || strings.Contains(name, "/") && suffix != "/" {
			continue
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		found = append(found, name)
	}
	return found
}

// FetchText retrieves a small text resource such as a checksum file or an index.
func FetchText(ctx context.Context, client *http.Client, url, userAgent string, logger *slog.Logger) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s: unexpected status %s", url, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", err
	}
	if logger != nil {
		logger.Debug("fetched", "url", url, "bytes", len(body))
	}
	return string(body), nil
}
