//go:build darwin

package verify

import (
	"context"
	"os/exec"
	"strings"
)

// checkSignature runs pkgutil and requires both a trusted status line and an
// Apple root in the chain. Anything else is a failure, not a warning: a file
// that claims to be an Apple installer and is not must never look acceptable.
func checkSignature(ctx context.Context, path string) (string, Status, error) {
	out, err := exec.CommandContext(ctx, "pkgutil", "--check-signature", path).CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		if ctx.Err() != nil {
			return "", Fail, ctx.Err()
		}
		return firstStatusLine(text, "pkgutil rejected the package"), Fail, nil
	}

	lower := strings.ToLower(text)
	trusted := strings.Contains(lower, "signed by a certificate trusted by") ||
		strings.Contains(lower, "signed apple software")
	appleRoot := strings.Contains(text, "Apple Root CA")

	switch {
	case trusted && appleRoot:
		return "Apple Root CA chain, trusted by macOS", Pass, nil
	case trusted:
		return firstStatusLine(text, "signed, but no Apple Root CA in the chain"), Fail, nil
	default:
		return firstStatusLine(text, "not signed by a trusted certificate"), Fail, nil
	}
}

// firstStatusLine pulls pkgutil's "Status:" line out of its output so errors
// stay to one line; fallback is used when there is no such line.
func firstStatusLine(out, fallback string) string {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Status:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "Status:"))
		}
	}
	if out == "" {
		return fallback
	}
	return fallback
}
