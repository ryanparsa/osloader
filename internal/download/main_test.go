package download

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain fails the package if a transfer leaves goroutines behind: a download
// manager that leaks one goroutine per chunk would not survive a long session.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		// The http transport parks idle connections briefly after Close.
		goleak.IgnoreTopFunction("net/http.(*persistConn).readLoop"),
		goleak.IgnoreTopFunction("net/http.(*persistConn).writeLoop"),
		goleak.IgnoreTopFunction("internal/poll.runtime_pollWait"),
	)
}
