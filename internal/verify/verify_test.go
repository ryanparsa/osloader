package verify

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "file.pkg")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestSHA256 pins the streaming hash against a known value.
func TestSHA256(t *testing.T) {
	path := writeTemp(t, "hello")
	got, err := SHA256(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	const want = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	if got != want {
		t.Errorf("sha256 = %s, want %s", got, want)
	}
}

// TestSizeMismatchFails is the check that catches a truncated or substituted
// file even when nothing else is available.
func TestSizeMismatchFails(t *testing.T) {
	path := writeTemp(t, "hello")

	report, err := ApplePkg(9999).Verify(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Failed() {
		t.Fatal("a wrong-sized file was reported as fine")
	}
	if got := report.Checks[0]; got.Name != "size" || got.Status != Fail {
		t.Errorf("first check = %+v, want a failing size check", got)
	}
	if report.SHA256 == "" {
		t.Error("the hash should still be reported for a failing file")
	}
}

// TestSignatureIsHardOnMacOS: on macOS a file that is not a signed Apple
// package must fail, not merely warn. Elsewhere the check cannot run, and must
// say so instead of implying success.
func TestSignatureIsHardOnMacOS(t *testing.T) {
	path := writeTemp(t, "not a package")

	report, err := ApplePkg(int64(len("not a package"))).Verify(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}

	var signature Check
	for _, c := range report.Checks {
		if c.Name == "signature" {
			signature = c
		}
	}
	if signature.Name == "" {
		t.Fatal("no signature check in the report")
	}

	if runtime.GOOS == "darwin" {
		if signature.Status != Fail || !report.Failed() {
			t.Errorf("unsigned file passed on macOS: %+v", signature)
		}
		return
	}
	if signature.Status != Warn {
		t.Errorf("signature status = %v, want Warn off macOS", signature.Status)
	}
	if report.Failed() {
		t.Error("a check that cannot run must not fail the file")
	}
}
