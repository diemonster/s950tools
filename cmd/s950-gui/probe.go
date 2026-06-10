// RS-232 auto-discovery — iterates USB-serial ports trying each
// common S950-compatible baud, sends RCAT, watches for an AKAI
// exclusive reply. Returns the first match. Defensive against
// non-S950 gear on shared buses: filters candidates by OS-level
// naming convention (USB-serial chips only — not Bluetooth or
// debug-console TTYs), and a non-matching reply is silently
// discarded rather than acted on (we saw a dev-board interpret
// inbound SysEx as a terminal command earlier in development).

package main

import (
	"errors"
	"fmt"
	"runtime"
	"strings"
	"time"

	"github.com/bivers/s950/internal/protocol"
	"github.com/bivers/s950/internal/transport"
)

// probeBauds are the rates an S950's UART can hit cleanly on
// firmware 1.2a, in descending order of speed. Slower rates last
// so a freshly-unboxed S950 (which defaults to 9600) still gets
// found, but a user who's bumped to 50000 doesn't pay the latency
// budget for the lower rates first.
var probeBauds = []int{50000, 38400, 19200, 9600}

// probeTimeout is the per-port-per-baud wait for a reply. The S950
// responds to RCAT within ~30 ms; 500 ms is comfortable headroom
// for USB-serial driver latency without bloating total probe time.
const probeTimeout = 500 * time.Millisecond

// ProbeResult is what ProbeForS950 returns on success.
type ProbeResult struct {
	Port string `json:"port"`
	Baud int    `json:"baud"`
}

// ProbeForS950 scans the OS-visible serial ports for an attached
// S950. Iterates candidate ports × candidate bauds, opens each
// briefly, sends RCAT, and returns on the first F0 47 reply. The
// probe respects the currently-open transport: if Connect already
// opened a port, we tear it down first since two opens of the same
// path is an error on most drivers.
//
// Returns the matched (port, baud) for the frontend to plug into
// its picker + auto-Connect. On no match returns an error
// containing the list of ports tried, so the user sees which
// candidates were inspected.
func (a *App) ProbeForS950() (*ProbeResult, error) {
	a.mu.Lock()
	a.closeTransportLocked()
	a.mu.Unlock()

	ports, err := a.listSerialPorts()
	if err != nil {
		return nil, fmt.Errorf("list serial ports: %w", err)
	}
	candidates := filterUSBSerialPorts(ports)
	if len(candidates) == 0 {
		return nil, errors.New("no USB-serial ports found — plug in your S950 cable")
	}

	rcat := protocol.BuildAkaiRequest(0, protocol.FuncRCAT, 0)
	tried := make([]string, 0, len(candidates)*len(probeBauds))
	for _, port := range candidates {
		for _, baud := range probeBauds {
			tried = append(tried, fmt.Sprintf("%s @ %d", port, baud))
			if ok := a.probeOne(port, baud, rcat); ok {
				return &ProbeResult{Port: port, Baud: baud}, nil
			}
		}
	}
	return nil, fmt.Errorf("no S950 found — tried %s", strings.Join(tried, ", "))
}

// probeOne opens `port` at `baud`, sends RCAT, and returns true if
// the response starts with the AKAI exclusive header (F0 47). Any
// other reply is treated as "not an S950" — non-matching gear stays
// untouched. Resources are released on every path. Method on App
// (rather than free function) so it routes through openSerial — the
// same injection seam Connect uses — for hermetic testing.
func (a *App) probeOne(port string, baud int, rcat []byte) bool {
	t, err := a.openSerial(transport.SerialOptions{Port: port, Baud: baud})
	if err != nil {
		return false
	}
	defer t.Close()

	// A short post-open settle is necessary on some FTDI drivers —
	// without it the first send sometimes vanishes into the UART
	// reset. 50ms is empirically enough; longer would balloon the
	// probe's total time across all bauds.
	time.Sleep(50 * time.Millisecond)
	if err := t.Send(rcat); err != nil {
		return false
	}
	msg, err := t.RecvSysEx(probeTimeout)
	if err != nil {
		return false
	}
	// AKAI exclusive starts with F0 47. We don't bother to fully
	// parse the catalog here — the protocol code will redo it as
	// the next operation once the frontend Connects.
	return len(msg) >= 2 && msg[0] == 0xF0 && msg[1] == 0x47
}

// filterUSBSerialPorts narrows the OS port list to entries that
// look like USB-serial adapters. The filter is naming-based per
// platform; not bulletproof, but eliminates the obvious noise
// (Bluetooth-Incoming-Port, debug-console, CDC-ACM dev-board
// "usbmodem" devices that aren't actually serial cables). False
// negatives can be remedied by the user picking the port manually
// from the dropdown — probe is convenience, not the only path.
func filterUSBSerialPorts(ports []string) []string {
	keep := make([]string, 0, len(ports))
	for _, p := range ports {
		if isLikelyUSBSerial(p) {
			keep = append(keep, p)
		}
	}
	return keep
}

func isLikelyUSBSerial(name string) bool {
	switch runtime.GOOS {
	case "darwin":
		// /dev/cu.usbserial-* (FTDI/PL2303); ignore tty.* duplicates
		// and Bluetooth/debug-console TTYs.
		return strings.Contains(name, "/cu.usbserial")
	case "linux":
		// /dev/ttyUSB* (USB-serial), /dev/ttyACM* skipped because
		// CDC-ACM is typically dev boards (the same false-positive
		// we hit on macOS with /dev/cu.usbmodem*).
		return strings.Contains(name, "/ttyUSB")
	case "windows":
		// COM<N> with no further qualifier on Windows. Probing all
		// COM ports is fine — terminal-mode dev boards are rare on
		// Windows and the user can always cancel a slow probe.
		return strings.HasPrefix(strings.ToUpper(name), "COM")
	}
	// Unknown OS — conservatively probe everything; the user can
	// always pick manually if probe misses.
	return true
}
