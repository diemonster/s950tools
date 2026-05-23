// Package transport wraps a MIDI input/output pair into a Transport that the
// S950 device layer can use to send raw MIDI bytes and receive complete
// inbound SysEx messages.
//
// We use gomidi/midi v2 + rtmididrv. The driver buffers SysEx until F7 is seen
// and only then delivers the message — this is fine for short responses
// (catalog, sample-parameter blocks, handshakes) and is also adequate for
// a full sample dump received in one piece. For the doc's "closed-loop"
// download flow (where the S950 pauses mid-SysEx waiting for ACKs between
// blocks) the device layer must pre-emptively stream ACKs, since rtmidi does
// not surface partial SysEx data.
package transport

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"gitlab.com/gomidi/midi/v2"
	"gitlab.com/gomidi/midi/v2/drivers"

	// Register the rtmidi driver.
	_ "gitlab.com/gomidi/midi/v2/drivers/rtmididrv"
)

// DefaultSysExBufferBytes is large enough to hold a maximum-size S950 sample
// dump (~966KB) with comfortable headroom.
const DefaultSysExBufferBytes = 2 * 1024 * 1024

// PortInfo is a name + numeric index pair.
type PortInfo struct {
	Index int
	Name  string
}

// ListPorts returns the names of available MIDI input and output ports.
func ListPorts() (ins, outs []PortInfo, err error) {
	for i, p := range midi.GetInPorts() {
		ins = append(ins, PortInfo{Index: i, Name: p.String()})
	}
	for i, p := range midi.GetOutPorts() {
		outs = append(outs, PortInfo{Index: i, Name: p.String()})
	}
	return ins, outs, nil
}

// Options controls how Open opens the in/out ports and configures listening.
type Options struct {
	// In is a substring matched (case-insensitive) against MIDI input port
	// names. If empty, the first available input port is used.
	In string
	// Out is the same for output.
	Out string
	// SysExBufferBytes sets the inbound SysEx buffer size. 0 means default.
	SysExBufferBytes uint32
	// Verbose, if true, hex-dumps every inbound SysEx to stderr via the
	// LogFunc (caller-supplied logger).
	Verbose bool
	// LogFunc is the destination for verbose hex dumps. If nil, no logs.
	LogFunc func(format string, args ...interface{})
}

// Transport is one open MIDI in/out pair ready for SysEx I/O.
type Transport struct {
	in   drivers.In
	out  drivers.Out
	stop func()
	opts Options

	mu  sync.Mutex
	rxQ []byte           // pending complete SysEx messages, concatenated
	rxC chan struct{}    // signalled whenever rxQ grows
	rxM []chan struct{}  // waiters
}

// Open opens the MIDI in and out ports matching the names in opts.
func Open(opts Options) (*Transport, error) {
	if opts.SysExBufferBytes == 0 {
		opts.SysExBufferBytes = DefaultSysExBufferBytes
	}
	ins := midi.GetInPorts()
	outs := midi.GetOutPorts()
	if len(ins) == 0 {
		return nil, errors.New("no MIDI input ports available")
	}
	if len(outs) == 0 {
		return nil, errors.New("no MIDI output ports available")
	}

	inPort, err := pickPortIn(ins, opts.In)
	if err != nil {
		return nil, fmt.Errorf("input port: %w", err)
	}
	outPort, err := pickPortOut(outs, opts.Out)
	if err != nil {
		return nil, fmt.Errorf("output port: %w", err)
	}

	if err := inPort.Open(); err != nil {
		return nil, fmt.Errorf("open in: %w", err)
	}
	if err := outPort.Open(); err != nil {
		_ = inPort.Close()
		return nil, fmt.Errorf("open out: %w", err)
	}

	t := &Transport{
		in:   inPort,
		out:  outPort,
		opts: opts,
		rxC:  make(chan struct{}, 1),
	}

	stop, err := midi.ListenTo(inPort, t.handleMessage,
		midi.UseSysEx(),
		midi.SysExBufferSize(opts.SysExBufferBytes),
	)
	if err != nil {
		_ = inPort.Close()
		_ = outPort.Close()
		return nil, fmt.Errorf("listen: %w", err)
	}
	t.stop = stop

	return t, nil
}

func pickPortIn(ports []drivers.In, want string) (drivers.In, error) {
	if want == "" {
		return ports[0], nil
	}
	w := strings.ToLower(want)
	for _, p := range ports {
		if strings.Contains(strings.ToLower(p.String()), w) {
			return p, nil
		}
	}
	names := make([]string, 0, len(ports))
	for _, p := range ports {
		names = append(names, p.String())
	}
	return nil, fmt.Errorf("no input port matches %q (have: %s)", want, strings.Join(names, ", "))
}

func pickPortOut(ports []drivers.Out, want string) (drivers.Out, error) {
	if want == "" {
		return ports[0], nil
	}
	w := strings.ToLower(want)
	for _, p := range ports {
		if strings.Contains(strings.ToLower(p.String()), w) {
			return p, nil
		}
	}
	names := make([]string, 0, len(ports))
	for _, p := range ports {
		names = append(names, p.String())
	}
	return nil, fmt.Errorf("no output port matches %q (have: %s)", want, strings.Join(names, ", "))
}

// handleMessage is invoked by gomidi for each parsed inbound message.
// For SysEx, data is the full F0..F7 envelope. We append it to rxQ verbatim.
func (t *Transport) handleMessage(msg midi.Message, _ int32) {
	data := []byte(msg)
	if len(data) == 0 {
		return
	}
	// We only care about SysEx for the S950 protocol.
	if data[0] != 0xF0 {
		return
	}
	if t.opts.Verbose && t.opts.LogFunc != nil {
		t.opts.LogFunc("RX (%d bytes): % X\n", len(data), prefixForLog(data))
	}
	t.mu.Lock()
	t.rxQ = append(t.rxQ, data...)
	waiters := t.rxM
	t.rxM = nil
	t.mu.Unlock()
	for _, w := range waiters {
		select {
		case w <- struct{}{}:
		default:
		}
	}
}

func prefixForLog(b []byte) []byte {
	const max = 64
	if len(b) <= max {
		return b
	}
	return b[:max]
}

// Send writes raw MIDI bytes to the output port. For SysEx, b must be a full
// F0..F7 message; rtmidi splits it across MIDIPacketLists internally as
// needed (CoreMIDI's per-list cap is 64KB).
func (t *Transport) Send(b []byte) error {
	if t.opts.Verbose && t.opts.LogFunc != nil {
		t.opts.LogFunc("TX (%d bytes): % X\n", len(b), prefixForLog(b))
	}
	return t.out.Send(b)
}

// RecvSysEx returns the next complete inbound SysEx message (F0..F7) or
// (nil, error) on timeout.
func (t *Transport) RecvSysEx(timeout time.Duration) ([]byte, error) {
	deadline := time.Now().Add(timeout)
	for {
		if msg, ok := t.popOneSysEx(); ok {
			return msg, nil
		}
		left := time.Until(deadline)
		if left <= 0 {
			return nil, fmt.Errorf("timeout waiting for SysEx (%v)", timeout)
		}
		t.mu.Lock()
		ch := make(chan struct{}, 1)
		t.rxM = append(t.rxM, ch)
		t.mu.Unlock()
		select {
		case <-ch:
		case <-time.After(left):
		}
	}
}

// popOneSysEx removes and returns the first complete SysEx from rxQ.
func (t *Transport) popOneSysEx() ([]byte, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.rxQ) == 0 {
		return nil, false
	}
	if t.rxQ[0] != 0xF0 {
		// Shouldn't happen since we only push F0-prefixed payloads.
		t.rxQ = t.rxQ[:0]
		return nil, false
	}
	// Find F7. The driver only pushes complete SysEx, so a terminator is
	// expected somewhere — but be defensive.
	for i := 1; i < len(t.rxQ); i++ {
		if t.rxQ[i] == 0xF7 {
			out := make([]byte, i+1)
			copy(out, t.rxQ[:i+1])
			t.rxQ = append([]byte(nil), t.rxQ[i+1:]...)
			return out, true
		}
	}
	return nil, false
}

// Drain discards any pending inbound bytes. Useful before starting a new
// request/response cycle.
func (t *Transport) Drain() {
	t.mu.Lock()
	t.rxQ = t.rxQ[:0]
	t.mu.Unlock()
}

// Close stops listening and releases the ports.
func (t *Transport) Close() error {
	if t.stop != nil {
		t.stop()
	}
	var errs []string
	if err := t.in.Close(); err != nil {
		errs = append(errs, "in: "+err.Error())
	}
	if err := t.out.Close(); err != nil {
		errs = append(errs, "out: "+err.Error())
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

// InName / OutName expose the names of the currently-open ports.
func (t *Transport) InName() string  { return t.in.String() }
func (t *Transport) OutName() string { return t.out.String() }
