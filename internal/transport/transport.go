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

// PortInfo is a name + numeric index pair returned by ListPorts.
type PortInfo struct {
	// Index is the driver's port index (stable for the lifetime of the
	// process; useful for selection by position).
	Index int
	// Name is the human-readable port name as reported by the OS / driver.
	Name string
}

// Options controls how Open opens the in/out ports and configures listening.
type Options struct {
	// In is a substring matched (case-insensitive) against MIDI input port
	// names. If empty, the first available input port is used.
	In string
	// Out is the same substring match for the output port.
	Out string
	// SysExBufferBytes sets the inbound SysEx buffer size. 0 means
	// DefaultSysExBufferBytes.
	SysExBufferBytes uint32
	// Verbose, if true, hex-dumps every inbound and outbound SysEx via the
	// LogFunc. No effect when LogFunc is nil.
	Verbose bool
	// LogFunc receives Printf-style verbose hex dumps. If nil, no logs are
	// emitted regardless of Verbose.
	LogFunc func(format string, args ...interface{})
	// OnWire is a structured callback fired for every inbound and outbound
	// SysEx. Direction is "tx" or "rx"; `msg` is the complete envelope
	// (`F0 …​ F7`). The byte slice is owned by the caller — copy if it
	// needs to outlive the call. Always fires when set, regardless of
	// Verbose; the GUI uses this to populate the wire-log panel without
	// the CLI's Printf-style sink getting in the way.
	OnWire func(direction string, msg []byte)
}

// PartialRecvTransport is an optional capability some transports
// (RS-232) implement: surfacing the raw inbound byte stream instead
// of only complete F0..F7 envelopes. The device layer uses this to
// do per-block synchronous ACKing on closed-loop sample dumps —
// MIDI's rtmidi driver doesn't expose mid-envelope bytes, so the
// MIDI path falls back to the pre-emptive ACK pump strategy.
//
// Usage contract: call BeginStream before driving a closed-loop
// receive, RecvBytes to drain the raw queue, EndStream when done.
// During a stream the envelope assembler is suspended — RecvSysEx
// returns nothing until EndStream restores it.
type PartialRecvTransport interface {
	Transport
	BeginStream()
	EndStream()
	RecvBytes(buf []byte, timeout time.Duration) (int, error)
}

// Transport is the protocol-level connection to an S950. Wire-format
// callers (the device layer) talk to this interface and never to a
// concrete implementation, so swapping MIDI for RS232 (or a future
// virtual / network bridge) is a constructor change, not a parser
// rewrite. The S950's protocol surface is identical across physical
// transports — same SysEx envelopes, same handshake codes — only the
// framing and flow-control semantics differ. Each method is safe to
// call from a single goroutine; concurrent users should serialise.
type Transport interface {
	// Send writes raw bytes to the device. For SysEx, b must be a
	// complete F0..F7 envelope on MIDI; on RS232 it may be sent
	// as a continuous byte stream without per-envelope framing
	// (still F0-prefixed, but F7 may be deferred until end-of-
	// dump for the closed-loop sample dump flow).
	Send(b []byte) error
	// RecvSysEx returns the next complete inbound SysEx envelope
	// (F0..F7) or an error on timeout. Implementations are
	// allowed to buffer/assemble depending on the underlying
	// transport — MIDI delivers atomic envelopes; RS232 reads
	// byte-by-byte and assembles.
	RecvSysEx(timeout time.Duration) ([]byte, error)
	// Drain discards any pending inbound bytes. Useful before
	// starting a new request/response cycle so a stale reply
	// doesn't get mistaken for the current one.
	Drain()
	// Close releases the underlying ports / serial handle.
	Close() error
	// InName / OutName return the human-readable port names
	// the transport was opened against — used by the GUI's
	// connection status chip and the wire log.
	InName() string
	OutName() string
}

// midiTransport is the gomidi+rtmidi backend — the implementation
// used by every consumer today. It delivers complete F0..F7 SysEx
// envelopes via gomidi's listener callback; partial SysEx receive
// is not supported (a hardware limitation of the underlying
// library, see docs/S950_CLI_HANDOFF.md and the Phase 1C notes).
type midiTransport struct {
	in   drivers.In
	out  drivers.Out
	stop func()
	opts Options

	mu  sync.Mutex
	rxQ []byte          // pending complete SysEx messages, concatenated
	rxC chan struct{}   // signalled whenever rxQ grows
	rxM []chan struct{} // waiters
}

// DefaultSysExBufferBytes is large enough to hold a maximum-size S950 sample
// dump (~966KB) with comfortable headroom.
const DefaultSysExBufferBytes = 2 * 1024 * 1024

// Open opens MIDI in/out ports matching the names in opts and starts the
// listener that delivers complete SysEx envelopes via RecvSysEx. Returns
// the Transport interface so callers stay decoupled from the MIDI backend
// — see the package docstring for the rationale.
func Open(opts Options) (Transport, error) {
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

	t := &midiTransport{
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

// Send writes raw MIDI bytes to the output port. For SysEx, b must be a full
// F0..F7 message; rtmidi splits it across MIDIPacketLists internally as
// needed (CoreMIDI's per-list cap is 64KB).
func (t *midiTransport) Send(b []byte) error {
	if t.opts.Verbose && t.opts.LogFunc != nil {
		t.opts.LogFunc("TX (%d bytes): % X\n", len(b), prefixForLog(b))
	}
	if t.opts.OnWire != nil {
		// Copy: callers should be free to retain the slice past the
		// send-call return without worrying about driver reuse.
		cp := make([]byte, len(b))
		copy(cp, b)
		t.opts.OnWire("tx", cp)
	}
	return t.out.Send(b)
}

// RecvSysEx returns the next complete inbound SysEx message (F0..F7) or
// (nil, error) on timeout.
func (t *midiTransport) RecvSysEx(timeout time.Duration) ([]byte, error) {
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

// Drain discards any pending inbound bytes. Useful before starting a new
// request/response cycle.
func (t *midiTransport) Drain() {
	t.mu.Lock()
	t.rxQ = t.rxQ[:0]
	t.mu.Unlock()
}

// Close stops listening and releases the ports.
func (t *midiTransport) Close() error {
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

// InName returns the name of the open input port.
func (t *midiTransport) InName() string { return t.in.String() }

// OutName returns the name of the open output port.
func (t *midiTransport) OutName() string { return t.out.String() }

// handleMessage is invoked by gomidi for each parsed inbound message.
// For SysEx, data is the full F0..F7 envelope. We append it to rxQ
// verbatim.
func (t *midiTransport) handleMessage(msg midi.Message, _ int32) {
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
	if t.opts.OnWire != nil {
		cp := make([]byte, len(data))
		copy(cp, data)
		t.opts.OnWire("rx", cp)
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

// popOneSysEx removes and returns the first complete SysEx from rxQ.
func (t *midiTransport) popOneSysEx() ([]byte, bool) {
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

func prefixForLog(b []byte) []byte {
	const max = 64
	if len(b) <= max {
		return b
	}
	return b[:max]
}
