package transport

import (
	"strings"
	"testing"
)

// virtualPortsSupportedOn is the pure seam behind
// VirtualMidiPortsSupported. The Windows=false case is load-bearing:
// RtMidi's WinMM backend treats openVirtualPort as a non-fatal
// warning and reports success WITHOUT creating a port, so without
// the explicit gate a Windows user would see an "active" virtual
// port no DAW can target.
func TestVirtualPortsSupportedOn(t *testing.T) {
	cases := []struct {
		goos string
		want bool
	}{
		{"darwin", true},  // CoreMIDI
		{"linux", true},   // ALSA sequencer
		{"windows", false}, // WinMM — no app-created ports
		{"freebsd", false}, // conservatively unsupported
		{"", false},
	}
	for _, c := range cases {
		if got := virtualPortsSupportedOn(c.goos); got != c.want {
			t.Errorf("virtualPortsSupportedOn(%q) = %v, want %v", c.goos, got, c.want)
		}
	}
}

func TestOpenVirtualMidiIn_ErrorMentionsLoopbackWorkaround(t *testing.T) {
	// On unsupported platforms the error must tell the user what to
	// do instead (loopback driver + physical-port path). We can't
	// flip GOOS at runtime, so this test only runs meaningfully on
	// Windows CI; on macOS/Linux it exercises the supported path's
	// driver lookup instead and is skipped if a real driver exists.
	if VirtualMidiPortsSupported() {
		t.Skip("virtual ports supported on this platform — gate not reachable")
	}
	_, err := OpenVirtualMidiIn("TEST", func([]byte) {})
	if err == nil {
		t.Fatal("expected error on unsupported platform")
	}
	if !strings.Contains(err.Error(), "loopMIDI") {
		t.Errorf("error should point at the loopback workaround, got: %v", err)
	}
}
