// RS-232 backend for the Transport interface. The S950 supports a
// serial control mode (overall settings field MIRS2 = 2) where the
// same SysEx envelopes used over MIDI flow point-to-point over an
// RS-232 cable. RS-232 unlocks two things MIDI couldn't:
//
//   1. We see the inbound byte stream as it arrives, not as atomic
//      F0..F7 envelopes — so a future Device implementation can do
//      true per-block ACKing on closed-loop sample dumps instead of
//      the pre-emptive pump hack.
//   2. The S950's RS-232 port runs faster than MIDI's 31250 baud
//      (configurable via overall-settings; common configs are
//      38400 / 76800 / 115200), so dumps complete in a fraction of
//      the time.
//
// This file only implements the byte-pipe; closed-loop synchronisation
// remains in the device/protocol layers.
package transport
//
// Wire framing on RS-232 is identical to MIDI SysEx — the same
// `F0 ... F7` envelopes the protocol package emits/parses — so this
// file's job is just to:
//
//   - Open the OS serial port at the configured baud/parity/etc.
//   - Spawn a reader goroutine that scans the incoming byte stream
//     for F0..F7 boundaries and pushes each complete envelope into
//     the same rxQ that RecvSysEx pops from.
//   - Send() writes raw bytes (already-framed envelopes from the
//     device layer) straight to the port.

import (
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"go.bug.st/serial"
)

// SerialOptions controls how OpenSerial opens the RS-232 port and
// what speed/framing it expects. Defaults match the S950's
// out-of-the-box settings (38400, 8N1, no flow control); override
// when the device has been reconfigured.
type SerialOptions struct {
	// Port is the OS-level device path (e.g. "/dev/cu.usbserial-XXXX"
	// on macOS, "COM3" on Windows, "/dev/ttyUSB0" on Linux). Required.
	Port string
	// Baud is the serial baud rate. The S950 supports 9600 / 19200 /
	// 38400 / 76800 / 115200 — match the value set in overall
	// settings on the device. Defaults to 38400.
	Baud int
	// FlowControl chooses the line-level flow control. The S950's
	// SDS-style protocol uses application-level ACK/NAK handshakes
	// for synchronisation, so most installs leave this off. Set to
	// "rtscts" if your cable wires the modem-control lines and the
	// device has been configured to honour them.
	FlowControl string // "" | "none" | "rtscts"
	// OnWire is called for every complete F0..F7 envelope sent or
	// received. Same callback shape as the MIDI Options.OnWire so a
	// caller (the GUI wire-log) can register one callback regardless
	// of the underlying transport.
	OnWire func(direction string, msg []byte)
	// LogFunc / Verbose mirror the MIDI Options — when both are set,
	// every envelope hex-dumps to LogFunc. Used by the CLI's
	// --verbose flag.
	Verbose bool
	LogFunc func(format string, args ...interface{})
}

// serialTransport is the RS-232 implementation of the Transport
// interface. It owns one open serial.Port plus a reader goroutine
// that scans for F0..F7 boundaries.
//
// The reader operates in one of two modes:
//   - envelope mode (default): inbound bytes are assembled into
//     complete F0..F7 envelopes and queued in rxQ for RecvSysEx.
//   - stream mode: inbound bytes go raw into rawQ for RecvBytes,
//     bypassing assembly entirely. Used by closed-loop sample dumps
//     where we need to ACK each block as its bytes arrive, not after
//     the trailing F7.
//
// Mode toggling is implemented as a flag the reader checks per byte,
// so callers can flip mid-conversation without coordinating with the
// reader goroutine.
type serialTransport struct {
	port   serial.Port
	opts   SerialOptions
	stopRx chan struct{}
	rxDone chan struct{}

	mu      sync.Mutex
	rxQ     [][]byte        // queued complete envelopes (envelope mode)
	rxM     []chan struct{} // RecvSysEx waiters
	stream  bool            // true = stream mode active
	rawQ    []byte          // raw bytes queued during stream mode
	rawM    []chan struct{} // RecvBytes waiters

	// txMu serialises writes to the port. Historically Send had a
	// single caller (the device layer, serialised by the App lock),
	// but MIDI-thru forwarding introduced a second writer goroutine
	// (the rtmidi listen callback). Without this lock, a 3-byte note
	// message could interleave INSIDE a SysEx envelope's bytes — a
	// non-realtime status byte mid-envelope terminates the SysEx per
	// the MIDI spec, corrupting uploads.
	txMu sync.Mutex
}

// OpenSerial opens the named serial port and starts the inbound byte
// reader. Returns the Transport interface so callers can route MIDI
// and RS-232 through the same code paths.
func OpenSerial(opts SerialOptions) (Transport, error) {
	if opts.Port == "" {
		return nil, errors.New("serial port name required")
	}
	if opts.Baud == 0 {
		opts.Baud = 38400
	}
	mode := &serial.Mode{
		BaudRate: opts.Baud,
		DataBits: 8,
		Parity:   serial.NoParity,
		StopBits: serial.OneStopBit,
		// Always assert DTR+RTS at open: many serial-attached devices
		// (and the S950 in particular) only consider the host
		// "connected" when these modem-control lines are high. Even in
		// no-flow-control mode they're treated as a presence signal —
		// without them, the device may silently ignore inbound data.
		InitialStatusBits: &serial.ModemOutputBits{RTS: true, DTR: true},
	}
	switch opts.FlowControl {
	case "", "none":
		// modem-control lines stay high (set above) but byte-level
		// pacing is not gated by RTS/CTS — the protocol's ACK/NAK
		// handshake handles synchronisation.
	case "rtscts":
		// rtscts mode is identical at the serial.Mode level today;
		// go.bug.st/serial doesn't expose hardware flow toggling on
		// all platforms, but the lines being high is what matters
		// for an S950 install that has hardware-flow wiring.
	default:
		return nil, fmt.Errorf("unsupported flow-control %q (expected none|rtscts)", opts.FlowControl)
	}

	p, err := serial.Open(opts.Port, mode)
	if err != nil {
		return nil, fmt.Errorf("open %s @ %d: %w", opts.Port, opts.Baud, err)
	}
	// A short read timeout lets the reader goroutine wake periodically
	// to check stopRx without holding the OS read forever. 100ms is
	// short enough that Close() doesn't feel laggy and long enough
	// that it doesn't burn CPU on quiet links.
	_ = p.SetReadTimeout(100 * time.Millisecond)

	t := &serialTransport{
		port:   p,
		opts:   opts,
		stopRx: make(chan struct{}),
		rxDone: make(chan struct{}),
	}
	go t.readLoop()
	return t, nil
}

// ListSerialPorts returns the OS-visible serial-port device names.
// Filtered list of all serial devices the OS exposes — the caller
// (CLI or GUI) decides which one is the S950 cable.
func ListSerialPorts() ([]string, error) {
	return serial.GetPortsList()
}

// sysexAssembler is a pure F0..F7 byte-stream parser. Feed it one
// byte at a time via Step; a non-nil return is a complete envelope
// (including the leading F0 and trailing F7). Stateful across calls
// — accumulates partial envelopes between Steps so a single
// envelope split across multiple OS reads still assembles.
//
// Behavior:
//   - F0 always resets the accumulator (mid-envelope F0 drops the
//     partial as the device must have aborted).
//   - Bytes outside an envelope are ignored — RS-232 shouldn't
//     normally produce any but defends against running-status
//     leakage if it does.
//   - The returned slice is a fresh copy; the caller owns it.
//
// Extracted from readLoop so its byte-level state machine can be
// unit-tested without spinning up a serial.Port.
type sysexAssembler struct {
	envelope []byte
	inSysEx  bool
}

func (a *sysexAssembler) Step(b byte) []byte {
	switch {
	case b == 0xF0:
		a.envelope = a.envelope[:0]
		a.envelope = append(a.envelope, b)
		a.inSysEx = true
	case a.inSysEx:
		a.envelope = append(a.envelope, b)
		if b == 0xF7 {
			out := make([]byte, len(a.envelope))
			copy(out, a.envelope)
			a.envelope = a.envelope[:0]
			a.inSysEx = false
			return out
		}
	}
	return nil
}

// readLoop pulls bytes from the serial port and assembles complete
// F0..F7 envelopes via sysexAssembler, posting each to rxQ as it
// lands. Any non-SysEx bytes between envelopes (running status /
// active sensing leakage — unlikely on RS-232 but possible) are
// discarded by the assembler.
func (t *serialTransport) readLoop() {
	defer close(t.rxDone)
	var (
		buf       = make([]byte, 1024)
		assembler sysexAssembler
	)
	for {
		select {
		case <-t.stopRx:
			return
		default:
		}
		n, err := t.port.Read(buf)
		if err != nil {
			// Closed port = clean shutdown; everything else surfaces
			// as a dropped read (caller will time out on RecvSysEx).
			var pe *serial.PortError
			if errors.Is(err, io.EOF) ||
				(errors.As(err, &pe) && pe.Code() == serial.PortClosed) {
				return
			}
			// Brief jitter on transient errors; loop will re-check stop.
			time.Sleep(10 * time.Millisecond)
			continue
		}
		if n == 0 {
			continue
		}
		// When verbose, surface raw inbound bytes — useful for
		// diagnosing wiring / baud-rate mismatches where we get
		// data but it never aligns to an F0..F7 envelope.
		if t.opts.Verbose && t.opts.LogFunc != nil {
			t.opts.LogFunc("RAW (%d bytes): % X\n", n, buf[:n])
		}
		// Snapshot the mode once per batch to keep the per-byte
		// branch cheap. A mid-batch toggle just takes effect on the
		// next OS read, which is fine — the BeginStream caller does
		// it before sending the request that triggers the stream.
		t.mu.Lock()
		streaming := t.stream
		t.mu.Unlock()
		if streaming {
			t.deliverRaw(buf[:n])
			continue
		}
		for i := 0; i < n; i++ {
			if env := assembler.Step(buf[i]); env != nil {
				t.deliver(env)
			}
		}
	}
}

// deliverRaw appends streamed bytes to rawQ and wakes any RecvBytes
// waiters. Bypasses the envelope assembler — used when the transport
// is in stream mode (BeginStream / EndStream).
func (t *serialTransport) deliverRaw(b []byte) {
	t.mu.Lock()
	t.rawQ = append(t.rawQ, b...)
	waiters := t.rawM
	t.rawM = nil
	t.mu.Unlock()
	for _, w := range waiters {
		select {
		case w <- struct{}{}:
		default:
		}
	}
}

// BeginStream switches the reader into raw-byte mode. Any bytes
// currently in the envelope queue are left alone; new inbound bytes
// go to rawQ until EndStream restores envelope assembly.
func (t *serialTransport) BeginStream() {
	t.mu.Lock()
	t.stream = true
	t.rawQ = t.rawQ[:0]
	t.mu.Unlock()
}

// EndStream restores envelope-assembly mode. Any partial bytes left
// in rawQ are discarded — the caller is responsible for fully
// draining the stream before calling EndStream.
func (t *serialTransport) EndStream() {
	t.mu.Lock()
	t.stream = false
	t.rawQ = t.rawQ[:0]
	t.mu.Unlock()
}

// RecvBytes copies up to len(buf) raw bytes from the stream queue,
// blocking until at least one byte arrives or timeout elapses.
// Returns (bytes-copied, nil) on success.
func (t *serialTransport) RecvBytes(buf []byte, timeout time.Duration) (int, error) {
	deadline := time.Now().Add(timeout)
	for {
		t.mu.Lock()
		if len(t.rawQ) > 0 {
			n := copy(buf, t.rawQ)
			t.rawQ = t.rawQ[n:]
			t.mu.Unlock()
			return n, nil
		}
		left := time.Until(deadline)
		if left <= 0 {
			t.mu.Unlock()
			return 0, fmt.Errorf("timeout waiting for bytes (%v)", timeout)
		}
		ch := make(chan struct{}, 1)
		t.rawM = append(t.rawM, ch)
		t.mu.Unlock()
		select {
		case <-ch:
		case <-time.After(left):
		}
	}
}

// deliver enqueues a completed envelope and wakes any RecvSysEx
// waiters. The hex-log / OnWire callback fire here so the wire log
// stays accurate even if no one's waiting at the moment.
func (t *serialTransport) deliver(env []byte) {
	if t.opts.Verbose && t.opts.LogFunc != nil {
		t.opts.LogFunc("RX (%d bytes): % X\n", len(env), prefixForLog(env))
	}
	if t.opts.OnWire != nil {
		t.opts.OnWire("rx", env)
	}
	t.mu.Lock()
	t.rxQ = append(t.rxQ, env)
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

// Send writes raw bytes to the serial port. The device layer hands
// us already-framed F0..F7 envelopes; we don't split or reframe.
func (t *serialTransport) Send(b []byte) error {
	if t.opts.Verbose && t.opts.LogFunc != nil {
		t.opts.LogFunc("TX (%d bytes): % X\n", len(b), prefixForLog(b))
	}
	if t.opts.OnWire != nil {
		cp := make([]byte, len(b))
		copy(cp, b)
		t.opts.OnWire("tx", cp)
	}
	t.txMu.Lock()
	defer t.txMu.Unlock()
	_, err := t.port.Write(b)
	return err
}

// WaitTX blocks until the OS has actually transmitted everything
// queued on the port (termios tcdrain / FlushFileBuffers). This is
// the exact replacement for the MIDI path's sleep-an-estimate drain:
// serial callers wait precisely as long as the UART needs, instead
// of a worst-case guess at MIDI's 3125 B/s. Discovered via the
// device layer's optional-capability assertion, so the Transport
// interface (and every test fake) stays unchanged.
func (t *serialTransport) WaitTX() error {
	return t.port.Drain()
}

// WireRate reports the line's payload throughput in bytes/sec
// (8N1: 10 bits on the wire per data byte). Lets the device layer
// compute honest progress estimates instead of assuming MIDI's
// 31250 baud.
func (t *serialTransport) WireRate() int {
	baud := t.opts.Baud
	if baud <= 0 {
		baud = 38400
	}
	return baud / 10
}

// SendQuiet writes bytes to the port without the wire-log callback.
// Used by the MIDI-thru forwarder: live performance traffic at note
// rates would flood the GUI's wire-log panel and bury the SysEx
// conversation the panel exists to show. Same tx serialisation as
// Send. Accessed via type assertion (see cmd/s950-gui/midithru.go)
// so the Transport interface — and every test fake implementing it —
// stays unchanged.
func (t *serialTransport) SendQuiet(b []byte) error {
	if t.opts.Verbose && t.opts.LogFunc != nil {
		t.opts.LogFunc("TX-thru (%d bytes): % X\n", len(b), prefixForLog(b))
	}
	t.txMu.Lock()
	defer t.txMu.Unlock()
	_, err := t.port.Write(b)
	return err
}

// RecvSysEx returns the next complete F0..F7 envelope or an error
// after timeout. Waiters are notified via a fresh channel each call
// so multiple goroutines blocked on RecvSysEx can coexist (the
// reader chooses one arbitrarily via the FIFO queue).
func (t *serialTransport) RecvSysEx(timeout time.Duration) ([]byte, error) {
	deadline := time.Now().Add(timeout)
	for {
		t.mu.Lock()
		if len(t.rxQ) > 0 {
			msg := t.rxQ[0]
			t.rxQ = t.rxQ[1:]
			t.mu.Unlock()
			return msg, nil
		}
		left := time.Until(deadline)
		if left <= 0 {
			t.mu.Unlock()
			return nil, fmt.Errorf("timeout waiting for SysEx (%v)", timeout)
		}
		ch := make(chan struct{}, 1)
		t.rxM = append(t.rxM, ch)
		t.mu.Unlock()
		select {
		case <-ch:
		case <-time.After(left):
		}
	}
}

// Drain discards any queued envelopes. Mirrors the MIDI transport's
// semantic — call before starting a new request/response cycle so
// a stale reply doesn't get attributed to the new request.
func (t *serialTransport) Drain() {
	t.mu.Lock()
	t.rxQ = t.rxQ[:0]
	t.mu.Unlock()
}

// Close stops the reader and releases the OS port. Idempotent.
func (t *serialTransport) Close() error {
	select {
	case <-t.stopRx:
		// already closed
		return nil
	default:
		close(t.stopRx)
	}
	err := t.port.Close()
	<-t.rxDone
	return err
}

// InName / OutName return the same port name — RS-232 is
// bidirectional on one cable, so "in" and "out" are aliases.
func (t *serialTransport) InName() string  { return t.opts.Port }
func (t *serialTransport) OutName() string { return t.opts.Port }
