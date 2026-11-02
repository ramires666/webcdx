// Package buildinfo contains build-time version metadata shared by the binaries.
package buildinfo

import "strings"

// Version is a development fallback. Release builds may override it with
// -ldflags "-X webcodex/internal/buildinfo.Version=<version>".
var Version = "dev"

// DisplayPeer returns the received peer version or an explicit legacy marker.
func DisplayPeer(version string) string {
	version = strings.TrimSpace(version)
	if version == "" {
		return "версия не передана"
	}
	version = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, version)
	if version == "" {
		return "версия не передана"
	}
	runes := []rune(version)
	if len(runes) > 80 {
		version = string(runes[:80])
	}
	return version
}
