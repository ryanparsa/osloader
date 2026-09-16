// Package debian lists and resolves Debian installer and live images from
// cdimage.debian.org. Debian publishes a SHA256SUMS file per directory (with a
// detached GPG signature alongside), which is what makes a download from any
// mirror verifiable.
package debian

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

// checksums maps a file name to its published SHA-256.
type checksums map[string]string

// parseChecksums reads the "<hash>  <filename>" lines of a SHA256SUMS file.
func parseChecksums(r io.Reader) checksums {
	sums := checksums{}
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 || !isHexDigest(fields[0]) {
			continue
		}
		sums[strings.TrimPrefix(fields[1], "*")] = strings.ToLower(fields[0])
	}
	return sums
}

// isHexDigest keeps stray text out of the checksum table: a SHA-256 is 64 hex
// characters and nothing else.
func isHexDigest(value string) bool {
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

// listedFile is what the directory index tells us beyond the checksum.
type listedFile struct {
	name     string
	size     int64
	modified time.Time
}

// indexRow matches one row of the Apache autoindex that cdimage serves.
var indexRow = regexp.MustCompile(
	`<a href="([^"]+)">[^<]*</a></td><td class="indexcollastmod">\s*([^<]*?)\s*</td><td class="indexcolsize">\s*([^<]*?)\s*</td>`)

// parseIndex pulls names, sizes and dates out of the directory listing. The
// sizes there are rounded ("756M"), so they are for display only - the digest
// is what a download is checked against.
func parseIndex(body string) map[string]listedFile {
	files := map[string]listedFile{}
	for _, row := range indexRow.FindAllStringSubmatch(body, -1) {
		name := row[1]
		if !strings.HasSuffix(name, ".iso") {
			continue
		}
		file := listedFile{name: name, size: parseHumanSize(row[3])}
		if when, err := time.Parse("2006-01-02 15:04", row[2]); err == nil {
			file.modified = when
		}
		files[name] = file
	}
	return files
}

// parseHumanSize turns the index's "756M" or "4.4G" into bytes.
func parseHumanSize(value string) int64 {
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

// fetchText retrieves a small text resource such as SHA256SUMS or an index.
func fetchText(ctx context.Context, client *http.Client, url, userAgent string, logger *slog.Logger) (string, error) {
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
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", err
	}
	logger.Debug("fetched", "url", url, "bytes", len(body))
	return string(body), nil
}

// imageName describes what a Debian image file actually is.
type imageName struct {
	version string
	variant string
}

var reVersion = regexp.MustCompile(`^\d+\.\d+(?:\.\d+)?$`)

// describe splits a Debian image name into its version and what the image is.
// The names follow "debian[-flavour]-<version>-<arch>-<variant>.iso", e.g.
// debian-13.7.0-amd64-netinst.iso, debian-edu-13.7.0-amd64-netinst.iso or
// debian-live-13.7.0-amd64-gnome.iso.
func describe(filename string) imageName {
	name := imageName{variant: "image"}
	parts := strings.Split(strings.TrimSuffix(filename, ".iso"), "-")

	version := -1
	for i, part := range parts {
		if reVersion.MatchString(part) {
			version = i
			break
		}
	}
	if version < 0 {
		return name
	}
	name.version = parts[version]

	// Anything after <version>-<arch> describes the image; anything between
	// "debian" and the version is a flavour such as edu, mac or live.
	if tail := parts[min(version+2, len(parts)):]; len(tail) > 0 {
		name.variant = strings.Join(tail, "-")
	}
	if flavour := parts[1:version]; len(flavour) > 0 {
		name.variant = strings.Join(flavour, " ") + " " + name.variant
	}
	return name
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
