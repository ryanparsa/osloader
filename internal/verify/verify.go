// Package verify answers one question: is the file on disk really the artifact
// the vendor published? Checks are reported individually so a caller can show
// what passed, what could not run, and what actually failed.
package verify

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"os"
	"strings"
)

// Status is the outcome of a single check.
type Status int

const (
	// Pass means the check ran and the file satisfied it.
	Pass Status = iota
	// Fail means the check ran and the file did not satisfy it.
	Fail
	// Warn means the check could not run here (e.g. pkgutil off macOS).
	Warn
	// Info means the check produced a value but gates nothing.
	Info
)

func (s Status) String() string {
	switch s {
	case Pass:
		return "ok"
	case Fail:
		return "FAILED"
	case Warn:
		return "unavailable"
	default:
		return "info"
	}
}

// Check is one named verification step and its outcome.
type Check struct {
	Name   string `json:"name"`
	Status Status `json:"-"`
	Result string `json:"status"`
	Detail string `json:"detail"`
}

// Report is the full set of checks run against a file.
type Report struct {
	Path   string  `json:"path"`
	Checks []Check `json:"checks"`
	SHA256 string  `json:"sha256,omitempty"`
}

// Failed reports whether any check ran and failed.
func (r *Report) Failed() bool {
	for _, c := range r.Checks {
		if c.Status == Fail {
			return true
		}
	}
	return false
}

// FailureReason returns the detail of the first failing check.
func (r *Report) FailureReason() string {
	for _, c := range r.Checks {
		if c.Status == Fail {
			return fmt.Sprintf("%s: %s", c.Name, c.Detail)
		}
	}
	return ""
}

func (r *Report) add(name string, status Status, format string, args ...any) {
	r.Checks = append(r.Checks, Check{
		Name:   name,
		Status: status,
		Result: status.String(),
		Detail: fmt.Sprintf(format, args...),
	})
}

// Verifier checks a downloaded file.
type Verifier interface {
	Verify(ctx context.Context, path string) (*Report, error)
}

// ApplePkg verifies a macOS installer package: exact size, then a streamed
// SHA-256 (informational only, since Apple publishes no per-file hash we can
// trust), then the package signature chain via pkgutil.
func ApplePkg(expectedSize int64) Verifier {
	return &applePkg{size: expectedSize}
}

type applePkg struct{ size int64 }

func (a *applePkg) Verify(ctx context.Context, path string) (*Report, error) {
	rep := &Report{Path: path}

	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	switch {
	case a.size <= 0:
		rep.add("size", Warn, "no expected size to compare against (%d bytes on disk)", fi.Size())
	case fi.Size() == a.size:
		rep.add("size", Pass, "%d bytes, matches the catalog", fi.Size())
	default:
		rep.add("size", Fail, "%d bytes on disk, catalog says %d", fi.Size(), a.size)
	}

	sum, err := SHA256(ctx, path)
	if err != nil {
		return nil, err
	}
	rep.SHA256 = sum
	rep.add("sha256", Info, "%s", sum)

	detail, status, err := checkSignature(ctx, path)
	if err != nil {
		return nil, err
	}
	rep.add("signature", status, "%s", detail)

	return rep, nil
}

// Checksum verifies a file against a digest the vendor published — the right
// gate when the download itself cannot be authenticated by its host, as with
// Debian's mirrors or Microsoft's plaintext CDN. source names where the digest
// came from, so the report can say why it is trustworthy.
func Checksum(algo, expected, source string, size int64) Verifier {
	return &checksum{algo: strings.ToLower(algo), expected: strings.ToLower(expected), source: source, size: size}
}

type checksum struct {
	algo     string
	expected string
	source   string
	size     int64
}

func (c *checksum) Verify(ctx context.Context, path string) (*Report, error) {
	rep := &Report{Path: path}

	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	switch {
	case c.size <= 0:
		rep.add("size", Info, "%d bytes (no published size to compare; the digest is the gate)", fi.Size())
	case fi.Size() == c.size:
		rep.add("size", Pass, "%d bytes, matches the published size", fi.Size())
	default:
		rep.add("size", Fail, "%d bytes on disk, vendor says %d", fi.Size(), c.size)
	}

	hasher, err := newHash(c.algo)
	if err != nil {
		return nil, err
	}
	sum, err := hashFile(ctx, path, hasher)
	if err != nil {
		return nil, err
	}
	if c.algo == "sha256" {
		rep.SHA256 = sum
	}

	switch {
	case c.expected == "":
		rep.add(c.algo, Warn, "%s (nothing published to compare against)", sum)
	case sum == c.expected:
		rep.add(c.algo, Pass, "matches %s", c.source)
	default:
		rep.add(c.algo, Fail, "got %s, %s says %s", sum, c.source, c.expected)
	}
	return rep, nil
}

func newHash(algo string) (hash.Hash, error) {
	switch algo {
	case "sha256":
		return sha256.New(), nil
	case "sha512":
		return sha512.New(), nil
	case "sha1":
		return sha1.New(), nil
	default:
		return nil, fmt.Errorf("unsupported digest %q", algo)
	}
}

// SHA256 streams the file through a hash with a fixed buffer, so memory use is
// constant regardless of file size.
func SHA256(ctx context.Context, path string) (string, error) {
	return hashFile(ctx, path, sha256.New())
}

// hashFile reads the file in fixed-size blocks: hashing a 17 GiB installer must
// cost the same memory as hashing a small one.
func hashFile(ctx context.Context, path string, h hash.Hash) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	buf := make([]byte, 1<<20)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, err := f.Read(buf)
		if n > 0 {
			h.Write(buf[:n])
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
