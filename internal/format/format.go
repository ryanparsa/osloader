// Package format renders byte counts, rates and durations the way a download
// UI should, and parses the rate flag back again.
package format

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"
)

// HumanBytes renders a byte count in binary units with one decimal.
func HumanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for size := n / unit; size >= unit && exp < 5; size /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// HumanRate renders a transfer speed.
func HumanRate(bytesPerSecond float64) string {
	if bytesPerSecond <= 0 || math.IsNaN(bytesPerSecond) || math.IsInf(bytesPerSecond, 0) {
		return "—"
	}
	return HumanBytes(int64(bytesPerSecond)) + "/s"
}

// HumanDuration keeps ETAs short: 1m23s, not 1m23.456789s.
func HumanDuration(d time.Duration) string {
	if d <= 0 {
		return "—"
	}
	d = d.Round(time.Second)
	if d >= time.Hour {
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
	if d >= time.Minute {
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%ds", int(d.Seconds()))
}

// ParseRate accepts values like "20M", "1.5MiB", "500k" as bytes per second.
func ParseRate(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	trimmed := strings.TrimSuffix(strings.TrimSuffix(s, "B"), "i")
	multiplier := int64(1)
	if trimmed != "" {
		switch trimmed[len(trimmed)-1] {
		case 'k', 'K':
			multiplier = 1 << 10
		case 'm', 'M':
			multiplier = 1 << 20
		case 'g', 'G':
			multiplier = 1 << 30
		}
	}
	if multiplier > 1 {
		trimmed = trimmed[:len(trimmed)-1]
	}
	value, err := strconv.ParseFloat(strings.TrimSpace(trimmed), 64)
	if err != nil || value < 0 {
		return 0, fmt.Errorf("invalid rate %q: use forms like 20M or 500k", s)
	}
	return int64(value * float64(multiplier)), nil
}

// IsTerminal reports whether stdout is a terminal, so progress can repaint one
// line there and print periodic lines when redirected.
func IsTerminal() bool {
	info, err := os.Stdout.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
