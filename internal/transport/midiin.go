// Standalone MIDI-input listeners — used by the GUI's MIDI-thru
// feature to receive live performance data (notes, CCs, pitch bend)
// from a controller or DAW and forward it down the RS-232 wire.
//
// Why this exists: hardware-verified (2026-06) — when the S950's
// front-panel controller-select is on RS-232C, the DIN MIDI input is
// completely dead (notes included), but the serial line accepts
// channel messages. So while the app holds an RS-232 session, the
// only way to play the sampler live is to route MIDI through the
// host. These listeners are independent of the control Transport —
// they only receive; forwarding policy (busy-drop, note-off flush)
// lives in the GUI layer.

package transport

import (
	"errors"
	"fmt"
	"runtime"

	"gitlab.com/gomidi/midi/v2"
	"gitlab.com/gomidi/midi/v2/drivers"
	"gitlab.com/gomidi/midi/v2/drivers/rtmididrv"
)

// MidiInListener is an open MIDI input delivering raw channel
// messages to a callback. Close releases the port (and, for virtual
// ports, removes the published CoreMIDI destination).
type MidiInListener struct {
	in   drivers.In
	stop func()
}

// Close stops listening and closes the underlying port. Idempotent
// enough for defer use — double-close errors from the driver are
// swallowed.
func (l *MidiInListener) Close() error {
	if l.stop != nil {
		l.stop()
		l.stop = nil
	}
	if l.in != nil {
		err := l.in.Close()
		l.in = nil
		return err
	}
	return nil
}

// listenChannelMessages wires the gomidi listener WITHOUT SysEx
// passthrough — thru forwarding is for performance data only.
// Forwarding inbound SysEx would let a DAW's bulk traffic interleave
// with (and corrupt) the app's own control conversation.
func listenChannelMessages(in drivers.In, onMsg func(msg []byte)) (*MidiInListener, error) {
	stop, err := midi.ListenTo(in, func(msg midi.Message, _ int32) {
		// midi.Message is a []byte of the complete message. Copy —
		// the driver may reuse the buffer after the callback returns.
		cp := make([]byte, len(msg))
		copy(cp, msg)
		onMsg(cp)
	})
	if err != nil {
		_ = in.Close()
		return nil, fmt.Errorf("listen: %w", err)
	}
	return &MidiInListener{in: in, stop: stop}, nil
}

// OpenMidiInListener opens the MIDI input whose name contains `port`
// (case-insensitive; empty picks the first available) and delivers
// every channel message to onMsg. The callback runs on the driver's
// goroutine — keep it fast and non-blocking.
func OpenMidiInListener(port string, onMsg func(msg []byte)) (*MidiInListener, error) {
	ins := midi.GetInPorts()
	if len(ins) == 0 {
		return nil, errors.New("no MIDI input ports available")
	}
	in, err := pickPortIn(ins, port)
	if err != nil {
		return nil, err
	}
	if err := in.Open(); err != nil {
		return nil, fmt.Errorf("open in: %w", err)
	}
	return listenChannelMessages(in, onMsg)
}

// VirtualMidiPortsSupported reports whether the host OS can publish
// virtual MIDI ports. CoreMIDI (macOS) and ALSA (Linux) support
// them; the Windows multimedia API has no concept of app-created
// MIDI ports. Pure GOOS switch — exported for the GUI's capability
// binding and for tests (virtualPortsSupportedOn is the testable
// seam).
func VirtualMidiPortsSupported() bool {
	return virtualPortsSupportedOn(runtime.GOOS)
}

func virtualPortsSupportedOn(goos string) bool {
	switch goos {
	case "darwin", "linux":
		return true
	}
	return false
}

// OpenVirtualMidiIn publishes a virtual MIDI destination named `name`
// (visible to DAWs as an output port they can target — e.g. Ableton's
// track-output list) and delivers everything sent to it to onMsg.
//
// macOS/Linux only. The Windows check MUST stay explicit: RtMidi's
// WinMM backend treats openVirtualPort as a non-fatal WARNING and
// returns success without creating anything — without this gate a
// Windows caller would report an active virtual port that no DAW can
// see (the worst failure mode: silently broken). Windows users
// bridge with a loopback driver (e.g. loopMIDI) and the physical-
// port path instead.
func OpenVirtualMidiIn(name string, onMsg func(msg []byte)) (*MidiInListener, error) {
	if !VirtualMidiPortsSupported() {
		return nil, fmt.Errorf(
			"virtual MIDI ports are not supported on %s — create a loopback port (e.g. loopMIDI) and select it as a regular input instead",
			runtime.GOOS)
	}
	drv, ok := drivers.Get().(*rtmididrv.Driver)
	if !ok {
		return nil, errors.New("virtual MIDI ports are not supported by this MIDI driver")
	}
	in, err := drv.OpenVirtualIn(name)
	if err != nil {
		return nil, fmt.Errorf("open virtual in %q: %w", name, err)
	}
	return listenChannelMessages(in, onMsg)
}
