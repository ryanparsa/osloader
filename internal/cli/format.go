package cli

import (
	"time"

	"github.com/ryanparsa/osloader/internal/format"
)

// Thin aliases so command code reads cleanly.
func humanBytes(n int64) string            { return format.HumanBytes(n) }
func humanRate(bps float64) string         { return format.HumanRate(bps) }
func humanDuration(d time.Duration) string { return format.HumanDuration(d) }
func parseRate(s string) (int64, error)    { return format.ParseRate(s) }
func isTerminal() bool                     { return format.IsTerminal() }
