// MIDI thru over RS-232 — forwards live performance data (notes,
// CCs, pitch bend) from a host MIDI input down the serial wire.
//
// Why: hardware-verified — with the S950's controller-select on
// RS-232C the DIN MIDI input is completely dead, but the serial line
// happily accepts channel messages (Device-mode preview relies on
// that). Without this feature the user has to walk to the front
// panel and flip controller-select every time they switch between
// "play it from Ableton" and "manage it from the app". With it, the
// app publishes a virtual MIDI destination (or taps an existing
// input) and the sampler stays playable through the whole session.
//
// Wire-safety policy:
//   • A forwarded message is sent while HOLDING App.mu (TryLock — a
//     held lock means a device operation owns the wire and the
//     message is dropped instead). Holding the lock across the send
//     closes two races at once: the transport can't be closed
//     between the busy check and the write, and a device op can't
//     start its SysEx conversation mid-forward.
//   • Dropped Note Offs / sustain releases are remembered and
//     flushed before the next forwarded message, so a held voice or
//     a down damper can't get stuck by an unlucky transfer. The
//     pending sets are only cleared when the flush actually reaches
//     the wire.
//   • Thru traffic bypasses the wire-log (SendQuiet) — note-rate
//     events would bury the SysEx conversation the log exists to
//     show.
//
// LOCK ORDER: App.mu BEFORE App.thruMu (thruMu is the innermost
// lock; nothing may acquire — or block on — App.mu while holding
// thruMu). The rtmidi callback runs on the driver's native thread
// and must never block on App.mu (TryLock only): MidiThruStop /
// Disconnect close the listener, and on some platforms (ALSA) that
// close JOINS the callback thread — a callback blocked on a lock the
// closer holds would deadlock the whole app. For the same reason the
// listener Close must never be called while holding thruMu.

package main

import (
	"fmt"
	"io"
	"time"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// VirtualThruPortName is the CoreMIDI destination name DAWs see when
// the virtual-port mode is active. Stable so Ableton track routings
// survive app restarts.
const VirtualThruPortName = "S950 RS-232 (s950-tools)"

// thruEventThrottle caps how often counter updates are pushed to the
// frontend. Forwarding itself is never throttled.
const thruEventThrottle = 500 * time.Millisecond

// MidiThruState is the payload of the "midithru:state" event and the
// MidiThruStatus binding.
type MidiThruState struct {
	Active    bool   `json:"active"`
	Source    string `json:"source"`  // port name or VirtualThruPortName
	Virtual   bool   `json:"virtual"`
	Forwarded int64  `json:"forwarded"`
	Dropped   int64  `json:"dropped"`
}

// thruState is the App-side bookkeeping for one active thru session.
// Guarded by App.thruMu (see the LOCK ORDER note in the file header).
type thruState struct {
	listener io.Closer
	source   string
	virtual  bool

	forwarded int64
	dropped   int64
	// held tracks sounding notes (key = channel<<8 | note) so Stop
	// can silence everything. pendingOff are Note Offs that were
	// dropped while the wire was busy — flushed, together with
	// pendingCtl, before the next forwarded message.
	held       map[uint16]struct{}
	pendingOff map[uint16]struct{}
	// pendingCtl are control releases dropped while busy, keyed
	// channel<<8 | controller: a dropped CC64 falling edge (damper
	// up) or CC123 (all notes off). Without these a transfer during
	// a pedal-up leaves the S950's damper down — note-offs alone
	// don't release damper-held voices.
	pendingCtl map[uint16]struct{}
	// damperDown tracks channels whose LAST WIRED CC64 value was
	// >= 64, so Stop can lift the damper as part of its silence
	// flush.
	damperDown map[byte]struct{}

	lastEmit time.Time
}

// MidiThruStart begins forwarding. virtual=true publishes the
// "S950 RS-232 (s950-tools)" virtual destination (macOS/Linux);
// otherwise `port` substring-matches an existing MIDI input. Only
// valid on serial sessions — on a MIDI session the DIN path is live
// and forwarding would double-trigger.
//
// Holds App.mu for the whole install so Disconnect can't tear the
// transport down between the kind check and the listener landing
// (the TOCTOU would otherwise leave a zombie thru session that
// survives into a later session). Opening an rtmidi port is
// milliseconds — acceptable under the lock for a user action.
func (a *App) MidiThruStart(port string, virtual bool) (*MidiThruState, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.kind != "serial" {
		return nil, fmt.Errorf("MIDI thru requires an RS-232 session (current transport: %s)", orNone(a.kind))
	}

	a.thruMu.Lock()
	running := a.thru != nil
	a.thruMu.Unlock()
	if running {
		return nil, fmt.Errorf("MIDI thru already running — stop it first")
	}

	var (
		listener io.Closer
		source   string
		err      error
	)
	if virtual {
		source = VirtualThruPortName
		listener, err = a.openThruVirtual(VirtualThruPortName, a.forwardThru)
	} else {
		source = port
		listener, err = a.openThruIn(port, a.forwardThru)
	}
	if err != nil {
		return nil, fmt.Errorf("start MIDI thru: %w", err)
	}

	a.thruMu.Lock()
	a.thru = &thruState{
		listener:   listener,
		source:     source,
		virtual:    virtual,
		held:       map[uint16]struct{}{},
		pendingOff: map[uint16]struct{}{},
		pendingCtl: map[uint16]struct{}{},
		damperDown: map[byte]struct{}{},
	}
	st := a.thruSnapshotLocked()
	// Emit inside thruMu: state events are serialised by the lock so
	// a racing stop's "inactive" push can't be overtaken by a stale
	// "active" one. EventsEmit dispatches async — safe under a lock.
	a.emitThruState(st)
	a.thruMu.Unlock()
	return &st, nil
}

// MidiThruStop ends forwarding and silences anything still sounding
// (held notes + down dampers). The silence flush runs on its own
// goroutine with a blocking App.mu acquisition: if a device transfer
// owns the wire, the flush lands right after it finishes instead of
// being abandoned (a stuck voice with no remedy) or blocking the UI.
func (a *App) MidiThruStop() (*MidiThruState, error) {
	a.thruMu.Lock()
	st := a.thru
	if st == nil {
		a.thruMu.Unlock()
		out := MidiThruState{}
		return &out, nil
	}
	a.thru = nil
	// Collect everything that may still be sounding.
	offs := make([]uint16, 0, len(st.held)+len(st.pendingOff))
	for k := range st.held {
		offs = append(offs, k)
	}
	for k := range st.pendingOff {
		offs = append(offs, k)
	}
	ctls := make([]uint16, 0, len(st.pendingCtl)+len(st.damperDown))
	for k := range st.pendingCtl {
		ctls = append(ctls, k)
	}
	for ch := range st.damperDown {
		ctls = append(ctls, uint16(ch)<<8|64)
	}
	out := MidiThruState{}
	a.emitThruState(out)
	a.thruMu.Unlock()

	// Close OUTSIDE thruMu: on some platforms the close joins the
	// callback thread, and an in-flight callback may be waiting on
	// thruMu — closing under the lock would deadlock permanently
	// (callback waits for thruMu, Close waits for callback).
	_ = st.listener.Close()

	// Asynchronous best-effort silence: blocking-lock the wire so a
	// busy transfer delays the flush rather than discarding it.
	if len(offs) > 0 || len(ctls) > 0 {
		go func() {
			a.mu.Lock()
			defer a.mu.Unlock()
			if a.tport != nil {
				sendSilence(a.tport, ctls, offs)
			}
		}()
	}
	return &out, nil
}

// MidiThruStatus reports the current thru state for the topbar chip.
// Also used by the frontend on mount to resync after a reload (the
// backend keeps forwarding across webview reloads; without the
// resync the chip would show "off" while notes still flow).
func (a *App) MidiThruStatus() *MidiThruState {
	a.thruMu.Lock()
	defer a.thruMu.Unlock()
	st := a.thruSnapshotLocked()
	return &st
}

// stopThruLocked tears the thru session down without the silence
// flush. Called with App.mu HELD (closeTransportLocked, Disconnect)
// when the transport goes away — there is no wire to flush onto.
// thruMu is released before the listener Close for the same
// join-deadlock reason as MidiThruStop; the callback can't wedge on
// App.mu because it only ever TryLocks it.
func (a *App) stopThruLocked() {
	a.thruMu.Lock()
	st := a.thru
	a.thru = nil
	if st != nil {
		a.emitThruState(MidiThruState{})
	}
	a.thruMu.Unlock()
	if st != nil {
		_ = st.listener.Close()
	}
}

// forwardThru is the rtmidi-listener callback: one MIDI channel
// message in, one (attempted) serial write out. Runs on the driver's
// native thread — must NEVER block on App.mu (TryLock only; see the
// file-header lock note).
func (a *App) forwardThru(msg []byte) {
	if len(msg) == 0 || msg[0] >= 0xF0 {
		// System / realtime traffic is not forwarded: SysEx would
		// interleave with the app's own control conversation, and
		// clock/active-sense are noise the S950 doesn't need.
		return
	}

	noteKey, isOn, isOff := classifyNote(msg)

	// Entry bookkeeping: record the note-on intent before the send
	// attempt so a Stop that interleaves still knows the voice may
	// be sounding.
	a.thruMu.Lock()
	if st := a.thru; st == nil {
		a.thruMu.Unlock()
		return
	} else if isOn {
		st.held[noteKey] = struct{}{}
	}
	a.thruMu.Unlock()

	// Busy gate + send, with App.mu HELD across the write: the
	// transport can't be closed underneath us and a device op can't
	// start mid-forward. A failed TryLock means the wire is owned —
	// drop, remembering anything that releases a voice.
	if !a.mu.TryLock() {
		a.thruMu.Lock()
		if st := a.thru; st != nil {
			st.dropped++
			if isOff {
				st.pendingOff[noteKey] = struct{}{}
				delete(st.held, noteKey)
			}
			recordDroppedControl(st, msg)
		}
		a.thruMu.Unlock()
		return
	}
	tport := a.tport
	if tport == nil {
		a.mu.Unlock()
		// Transport gone (mid-disconnect): forget the optimistic
		// note-on — nothing reached the wire.
		a.thruMu.Lock()
		if st := a.thru; st != nil && isOn {
			delete(st.held, noteKey)
		}
		a.thruMu.Unlock()
		return
	}

	// Liveness re-check + pending-flush snapshot, nested inside
	// App.mu (the allowed order). If Stop ran while we waited, the
	// session is gone — sending now would land AFTER the silence
	// flush and stick a voice nothing tracks anymore.
	a.thruMu.Lock()
	st := a.thru
	if st == nil {
		a.thruMu.Unlock()
		a.mu.Unlock()
		return
	}
	var flushOffs, flushCtls []uint16
	if len(st.pendingOff) > 0 {
		flushOffs = make([]uint16, 0, len(st.pendingOff))
		for k := range st.pendingOff {
			flushOffs = append(flushOffs, k)
		}
		st.pendingOff = map[uint16]struct{}{}
	}
	if len(st.pendingCtl) > 0 {
		flushCtls = make([]uint16, 0, len(st.pendingCtl))
		for k := range st.pendingCtl {
			flushCtls = append(flushCtls, k)
		}
		st.pendingCtl = map[uint16]struct{}{}
	}
	a.thruMu.Unlock()

	// The clears above are safe: we hold the wire (App.mu) and the
	// transport is non-nil, so these sends cannot be skipped — only
	// a hard write error loses them, and a broken serial line has
	// bigger problems than a hanging voice.
	sendSilence(tport, flushCtls, flushOffs)
	sendErr := sendQuiet(tport, msg)
	a.mu.Unlock()

	// Post bookkeeping + throttled emit.
	a.thruMu.Lock()
	if st := a.thru; st != nil {
		if sendErr == nil {
			st.forwarded++
			if isOff {
				delete(st.held, noteKey)
			}
			trackSentControl(st, msg)
		}
		if time.Since(st.lastEmit) >= thruEventThrottle {
			st.lastEmit = time.Now()
			a.emitThruState(a.thruSnapshotLocked())
		}
	}
	a.thruMu.Unlock()
}

// sendSilence writes control releases then note-offs. No locking —
// the caller must own the wire (App.mu held, transport non-nil).
// Controls first: lifting the damper before the note-offs is what
// actually releases damper-held voices.
func sendSilence(t interface{ Send([]byte) error }, ctls, offs []uint16) {
	for _, k := range ctls {
		ch := byte(k>>8) & 0x0F
		cc := byte(k & 0x7F)
		_ = sendQuiet(t, []byte{0xB0 | ch, cc, 0x00})
	}
	for _, k := range offs {
		ch := byte(k>>8) & 0x0F
		note := byte(k & 0x7F)
		_ = sendQuiet(t, []byte{0x80 | ch, note, 0x40})
	}
}

// classifyNote extracts note bookkeeping facts from a channel
// message: its (channel, note) key and whether it begins or ends a
// voice. Note On with velocity 0 is Note Off per the MIDI spec.
func classifyNote(msg []byte) (key uint16, isOn, isOff bool) {
	if len(msg) < 3 {
		return 0, false, false
	}
	status := msg[0] & 0xF0
	ch := uint16(msg[0] & 0x0F)
	key = ch<<8 | uint16(msg[1]&0x7F)
	switch {
	case status == 0x90 && msg[2] > 0:
		return key, true, false
	case status == 0x80, status == 0x90 && msg[2] == 0:
		return key, false, true
	}
	return 0, false, false
}

// recordDroppedControl remembers wire-releasing controls that were
// dropped while busy: a CC64 falling edge (damper up) or CC123 (all
// notes off). Caller holds thruMu.
func recordDroppedControl(st *thruState, msg []byte) {
	if len(msg) < 3 || msg[0]&0xF0 != 0xB0 {
		return
	}
	ch := uint16(msg[0] & 0x0F)
	switch msg[1] {
	case 64:
		if msg[2] < 64 {
			st.pendingCtl[ch<<8|64] = struct{}{}
		}
		// A dropped damper-DOWN is deliberately forgotten: re-
		// pressing a pedal after a transfer is natural; re-sounding
		// it automatically would be surprising.
	case 123:
		st.pendingCtl[ch<<8|123] = struct{}{}
	}
}

// trackSentControl updates per-channel damper state after a control
// actually reached the wire, so Stop knows which channels need a
// CC64 release. Caller holds thruMu.
func trackSentControl(st *thruState, msg []byte) {
	if len(msg) < 3 || msg[0]&0xF0 != 0xB0 || msg[1] != 64 {
		return
	}
	ch := byte(msg[0] & 0x0F)
	if msg[2] >= 64 {
		st.damperDown[ch] = struct{}{}
	} else {
		delete(st.damperDown, ch)
	}
}

// sendQuiet routes through the transport's wire-log-free path when
// it has one (the serial transport does), falling back to Send. The
// interface assertion keeps transport.Transport — and every test
// fake — unchanged.
func sendQuiet(t interface{ Send([]byte) error }, b []byte) error {
	if q, ok := t.(interface{ SendQuiet([]byte) error }); ok {
		return q.SendQuiet(b)
	}
	return t.Send(b)
}

func (a *App) thruSnapshotLocked() MidiThruState {
	if a.thru == nil {
		return MidiThruState{}
	}
	return MidiThruState{
		Active:    true,
		Source:    a.thru.source,
		Virtual:   a.thru.virtual,
		Forwarded: a.thru.forwarded,
		Dropped:   a.thru.dropped,
	}
}

func (a *App) emitThruState(st MidiThruState) {
	if a.ctx == nil {
		return
	}
	wruntime.EventsEmit(a.ctx, "midithru:state", st)
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}
