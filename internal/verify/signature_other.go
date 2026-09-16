//go:build !darwin

package verify

import "context"

// checkSignature cannot run away from macOS: pkgutil ships with the OS. It
// reports Warn rather than Pass, because a check that did not run must not be
// presented as one that succeeded.
func checkSignature(_ context.Context, _ string) (string, Status, error) {
	return "pkgutil is macOS-only; signature not checked on this host", Warn, nil
}
