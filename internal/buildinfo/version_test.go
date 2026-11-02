package buildinfo

import (
	"strings"
	"testing"
)

func TestDisplayPeer(t *testing.T) {
	if got := DisplayPeer("1.2.3"); got != "1.2.3" {
		t.Fatalf("DisplayPeer(version) = %q", got)
	}
	if got := DisplayPeer(""); got != "версия не передана" {
		t.Fatalf("DisplayPeer(legacy) = %q", got)
	}
	if got := DisplayPeer("\r\n\x00"); got != "версия не передана" {
		t.Fatalf("DisplayPeer(control-only) = %q", got)
	}
	if got := DisplayPeer(strings.Repeat("я", 81)); len([]rune(got)) != 80 {
		t.Fatalf("DisplayPeer(long) rune length = %d", len([]rune(got)))
	}
}
