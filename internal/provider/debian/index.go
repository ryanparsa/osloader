// Package debian lists and resolves Debian installer and live images from
// cdimage.debian.org. Debian publishes a SHA256SUMS file per directory (with a
// detached GPG signature alongside), which is what makes a download from any
// mirror verifiable.
package debian

import (
	"regexp"
	"strings"
)

// Debian's directories are read through the shared webdir helpers; what is
// left here is the part specific to Debian's file names.

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
