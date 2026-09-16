// Package windows registers a placeholder Windows provider for the Microsoft
// ISO catalogue, to be implemented behind the same interface as macOS.
package windows

import "github.com/ryanparsa/osloader/internal/provider"

func init() {
	provider.Register("windows", provider.Stub{
		OS:          "Windows",
		ChannelList: []provider.Channel{{ID: "retail", Label: "Retail ISOs"}},
	})
}
