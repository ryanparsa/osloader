// Package ua holds the User-Agent strings osloader may present. Being honest
// about who we are is the default; some networks and CDNs treat unknown agents
// differently, so a browser string is available as a preset.
package ua

import "strings"

const (
	// Default identifies osloader itself.
	Default = "osloader/0.1 (+https://github.com/ryanparsa/osloader)"

	// Browser is a current desktop Chrome on macOS, for servers that only
	// answer familiar clients.
	Browser = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 " +
		"(KHTML, like Gecko) Chrome/153.0.0.0 Safari/537.36"
)

// Resolve maps a flag value to a User-Agent: empty or "default" keeps ours,
// "browser"/"chrome"/"safari" uses the browser preset, anything else is sent
// verbatim.
func Resolve(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "default", "osloader":
		return Default
	case "browser", "chrome", "safari", "mozilla":
		return Browser
	default:
		return value
	}
}
