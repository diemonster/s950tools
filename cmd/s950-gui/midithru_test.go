package main

import (
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bivers/s950/internal/transport"
)

// Tests for the MIDI-thru forwarder. The listener seam (openThruIn /
// openThruVirtual) is faked so no rtmidi is needed: the fake captures
// the onMsg callback, and tests drive it directly as if the driver
// delivered messages. The transport is the connect_test fakes, so
// forwarded bytes are observable via sent[].

// thruRecorder is the fake transport for thru tests. Extends
// fakePingTransport with a recorded quiet-send path so tests can
// verify thru traffic uses SendQuiet (no wire-log) rather than Send.
type thruRecorder struct {
	fakePingTransport
	mu        sync.Mutex
	quietSent [][]byte
}

func (r *thruRecorder) SendQuiet(b []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := make([]byte, len(b))
	copy(cp, b)
	r.quietSent = append(r.quietSent, cp)
	return nil
}

func (r *thruRecorder) quiet() [][]byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([][]byte, len(r.quietSent))
	copy(out, r.quietSent)
	return out
}

// fakeListener is the io.Closer the faked openers hand back.
type fakeListener struct{ closed bool }

func (l *fakeListener) Close() error { l.closed = true; return nil }

// thruApp builds a serial-connected App with faked thru openers.
// Returns the app, the transport recorder, the captured onMsg
// callback (drive it to simulate inbound MIDI), and the listener.
func thruApp(t *testing.T) (*App, *thruRecorder, *func([]byte), *fakeListener) {
	t.Helper()
	rec := &thruRecorder{}
	listener := &fakeListener{}
	var onMsg func([]byte)
	app := withOpeners(nil, staticSerialOpener(rec, nil), nil)
	app.openThruIn = func(port string, cb func([]byte)) (io.Closer, error) {
		onMsg = cb
		return listener, nil
	}
	app.openThruVirtual = func(name string, cb func([]byte)) (io.Closer, error) {
		onMsg = cb
		return listener, nil
	}
	if err := app.ConnectSerial("/dev/fake", 50000); err != nil {
		t.Fatalf("ConnectSerial: %v", err)
	}
	return app, rec, &onMsg, listener
}

func TestMidiThruStart_RequiresSerialSession(t *testing.T) {
	app := withOpeners(
		func(o transport.Options) (transport.Transport, error) { return &fakePingTransport{}, nil },
		nil, nil)
	app.openThruIn = func(string, func([]byte)) (io.Closer, error) {
		t.Fatal("listener must not open on a MIDI session")
		return nil, nil
	}
	if err := app.Connect("", "", 0); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if _, err := app.MidiThruStart("AnyPort", false); err == nil {
		t.Fatal("expected error starting thru on a MIDI transport")
	}
}

func TestMidiThruStart_RejectsDoubleStart(t *testing.T) {
	app, _, _, _ := thruApp(t)
	if _, err := app.MidiThruStart("X", false); err != nil {
		t.Fatalf("first start: %v", err)
	}
	if _, err := app.MidiThruStart("X", false); err == nil {
		t.Fatal("second start should fail while running")
	}
}

func TestMidiThru_ForwardsChannelMessagesQuietly(t *testing.T) {
	app, rec, onMsg, _ := thruApp(t)
	if _, err := app.MidiThruStart("Controller", false); err != nil {
		t.Fatalf("start: %v", err)
	}
	noteOn := []byte{0x90, 60, 100}
	bend := []byte{0xE0, 0x00, 0x40}
	(*onMsg)(noteOn)
	(*onMsg)(bend)

	q := rec.quiet()
	if len(q) != 2 {
		t.Fatalf("expected 2 quiet sends, got %d", len(q))
	}
	if q[0][0] != 0x90 || q[1][0] != 0xE0 {
		t.Errorf("forwarded bytes wrong: % X / % X", q[0], q[1])
	}
	// Nothing through the loud Send path (wire-log would fire there).
	if len(rec.sent) != 0 {
		t.Errorf("thru traffic leaked into Send (wire-log path): %d msgs", len(rec.sent))
	}
	st := app.MidiThruStatus()
	if st.Forwarded != 2 || st.Dropped != 0 {
		t.Errorf("status = %+v, want forwarded=2 dropped=0", st)
	}
}

func TestMidiThru_IgnoresSystemMessages(t *testing.T) {
	app, rec, onMsg, _ := thruApp(t)
	if _, err := app.MidiThruStart("Controller", false); err != nil {
		t.Fatalf("start: %v", err)
	}
	(*onMsg)([]byte{0xF8})                   // clock
	(*onMsg)([]byte{0xFE})                   // active sense
	(*onMsg)([]byte{0xF0, 0x47, 0xF7})       // SysEx
	if got := len(rec.quiet()); got != 0 {
		t.Errorf("system messages must not be forwarded, got %d sends", got)
	}
}

func TestMidiThru_DropsWhileWireBusy_FlushesPendingNoteOffs(t *testing.T) {
	app, rec, onMsg, _ := thruApp(t)
	if _, err := app.MidiThruStart("Controller", false); err != nil {
		t.Fatalf("start: %v", err)
	}

	// Note On lands while the wire is free.
	(*onMsg)([]byte{0x90, 60, 100})
	if len(rec.quiet()) != 1 {
		t.Fatalf("note-on should forward, got %d", len(rec.quiet()))
	}

	// Simulate a device operation holding the wire.
	app.mu.Lock()
	(*onMsg)([]byte{0x80, 60, 0x40}) // its Note Off — dropped
	(*onMsg)([]byte{0x90, 62, 100})  // another Note On — dropped
	app.mu.Unlock()

	st := app.MidiThruStatus()
	if st.Dropped != 2 {
		t.Errorf("dropped = %d, want 2", st.Dropped)
	}
	if len(rec.quiet()) != 1 {
		t.Fatalf("nothing should hit the wire while busy, got %d", len(rec.quiet()))
	}

	// Next message after the wire frees: the stranded Note Off for
	// 60 must flush BEFORE the new message.
	(*onMsg)([]byte{0x90, 64, 90})
	q := rec.quiet()
	if len(q) != 3 {
		t.Fatalf("expected flush(off 60) + note-on 64, total 3 sends, got %d", len(q))
	}
	if q[1][0] != 0x80 || q[1][1] != 60 {
		t.Errorf("expected flushed Note Off for 60 first, got % X", q[1])
	}
	if q[2][0] != 0x90 || q[2][1] != 64 {
		t.Errorf("expected note-on 64 after flush, got % X", q[2])
	}
}

// waitForQuiet polls the recorder until it holds at least n messages
// or the deadline passes — Stop's silence flush runs on its own
// goroutine (blocking-lock so a busy wire delays rather than
// discards it), so tests must wait for it.
func waitForQuiet(t *testing.T, rec *thruRecorder, n int) [][]byte {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if q := rec.quiet(); len(q) >= n {
			return q
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d quiet sends (have %d)", n, len(rec.quiet()))
	return nil
}

func TestMidiThruStop_SilencesHeldNotes(t *testing.T) {
	app, rec, onMsg, listener := thruApp(t)
	if _, err := app.MidiThruStart("Controller", false); err != nil {
		t.Fatalf("start: %v", err)
	}
	// Two notes held down, one released.
	(*onMsg)([]byte{0x90, 60, 100})
	(*onMsg)([]byte{0x91, 36, 100}) // channel 1
	(*onMsg)([]byte{0x90, 64, 100})
	(*onMsg)([]byte{0x80, 64, 0x40}) // 64 released

	before := len(rec.quiet())
	if _, err := app.MidiThruStop(); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if !listener.closed {
		t.Error("listener not closed on stop")
	}
	flushed := waitForQuiet(t, rec, before+2)[before:]
	if len(flushed) != 2 {
		t.Fatalf("expected note-offs for the 2 held notes, got %d: %v", len(flushed), flushed)
	}
	got := map[string]bool{}
	for _, m := range flushed {
		if m[0]&0xF0 != 0x80 {
			t.Errorf("flush message not a Note Off: % X", m)
		}
		got[strings.ToUpper(string([]byte{m[0] & 0x0F, m[1]}))] = true
	}
	if !got[string([]byte{0x00, 60})] || !got[string([]byte{0x01, 36})] {
		t.Errorf("expected offs for ch0/60 + ch1/36, got %v", flushed)
	}
	// Status resets.
	st := app.MidiThruStatus()
	if st.Active {
		t.Error("status still active after stop")
	}
}

func TestMidiThruStop_FlushWaitsForBusyWire(t *testing.T) {
	// Stop while a device op holds the wire: the silence flush must
	// land AFTER the op releases App.mu — delayed, not discarded.
	app, rec, onMsg, _ := thruApp(t)
	if _, err := app.MidiThruStart("Controller", false); err != nil {
		t.Fatalf("start: %v", err)
	}
	(*onMsg)([]byte{0x90, 60, 100}) // held note, sent
	before := len(rec.quiet())

	app.mu.Lock() // device op owns the wire
	if _, err := app.MidiThruStop(); err != nil {
		t.Fatalf("stop: %v", err)
	}
	// Flush must NOT have happened yet.
	time.Sleep(30 * time.Millisecond)
	if got := len(rec.quiet()); got != before {
		t.Fatalf("flush ran while wire was busy: %d sends", got-before)
	}
	app.mu.Unlock() // op finishes

	flushed := waitForQuiet(t, rec, before+1)[before:]
	if flushed[0][0]&0xF0 != 0x80 || flushed[0][1] != 60 {
		t.Errorf("expected deferred Note Off 60, got % X", flushed[0])
	}
}

func TestMidiThru_SustainReleaseSurvivesBusyDrop(t *testing.T) {
	// Damper down (sent), pedal-up dropped during a transfer: the
	// release must flush before the next forwarded message —
	// note-offs alone don't release damper-held voices.
	app, rec, onMsg, _ := thruApp(t)
	if _, err := app.MidiThruStart("Controller", false); err != nil {
		t.Fatalf("start: %v", err)
	}
	(*onMsg)([]byte{0xB0, 64, 127}) // damper down — forwarded

	app.mu.Lock()
	(*onMsg)([]byte{0xB0, 64, 0}) // pedal up — dropped
	app.mu.Unlock()

	(*onMsg)([]byte{0x90, 60, 100}) // next message after wire frees
	q := rec.quiet()
	if len(q) != 3 {
		t.Fatalf("expected damper-down + flushed pedal-up + note-on, got %d: %v", len(q), q)
	}
	if q[1][0] != 0xB0 || q[1][1] != 64 || q[1][2] != 0 {
		t.Errorf("expected flushed CC64=0 before the note, got % X", q[1])
	}
}

func TestMidiThruStop_LiftsDamperHeldOnWire(t *testing.T) {
	// Damper was wired down and never released: Stop's silence pass
	// must send CC64=0 for that channel ahead of the note-offs.
	app, rec, onMsg, _ := thruApp(t)
	if _, err := app.MidiThruStart("Controller", false); err != nil {
		t.Fatalf("start: %v", err)
	}
	(*onMsg)([]byte{0xB0, 64, 127}) // damper down
	(*onMsg)([]byte{0x90, 60, 100}) // held note
	before := len(rec.quiet())
	if _, err := app.MidiThruStop(); err != nil {
		t.Fatalf("stop: %v", err)
	}
	flushed := waitForQuiet(t, rec, before+2)[before:]
	if flushed[0][0] != 0xB0 || flushed[0][1] != 64 || flushed[0][2] != 0 {
		t.Errorf("expected CC64=0 first in the silence flush, got % X", flushed[0])
	}
	if flushed[1][0]&0xF0 != 0x80 || flushed[1][1] != 60 {
		t.Errorf("expected Note Off 60 after the damper lift, got % X", flushed[1])
	}
}

func TestMidiThru_PendingOffSurvivesRepeatedBusyWindows(t *testing.T) {
	// A dropped Note Off must not be lost when the wire is STILL
	// busy at the next opportunity — pending sets are only cleared
	// once the flush actually reaches the wire.
	app, rec, onMsg, _ := thruApp(t)
	if _, err := app.MidiThruStart("Controller", false); err != nil {
		t.Fatalf("start: %v", err)
	}
	(*onMsg)([]byte{0x90, 60, 100}) // sent

	app.mu.Lock()
	(*onMsg)([]byte{0x80, 60, 0x40}) // dropped → pendingOff
	(*onMsg)([]byte{0x90, 62, 100})  // dropped too (still busy)
	app.mu.Unlock()

	(*onMsg)([]byte{0x90, 64, 90}) // wire free: flush + send
	q := rec.quiet()
	if len(q) != 3 {
		t.Fatalf("expected on60 + flushed off60 + on64, got %d: %v", len(q), q)
	}
	if q[1][0]&0xF0 != 0x80 || q[1][1] != 60 {
		t.Errorf("expected flushed off-60, got % X", q[1])
	}
}

func TestMidiThru_TransportGoneForgetsOptimisticNoteOn(t *testing.T) {
	// Note arriving after the transport is nil (mid-teardown window)
	// must not leave a phantom entry in held — nothing reached the
	// wire, so Stop must not "silence" it later.
	app, rec, onMsg, _ := thruApp(t)
	if _, err := app.MidiThruStart("Controller", false); err != nil {
		t.Fatalf("start: %v", err)
	}
	// Simulate the transport vanishing while thru is still installed.
	app.mu.Lock()
	app.tport = nil
	app.mu.Unlock()

	(*onMsg)([]byte{0x90, 60, 100})
	before := len(rec.quiet())
	if _, err := app.MidiThruStop(); err != nil {
		t.Fatalf("stop: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if got := len(rec.quiet()); got != before {
		t.Errorf("no silence flush expected (note never sounded), got %d extra", got-before)
	}
}

func TestMidiThru_StopsOnDisconnect(t *testing.T) {
	app, _, _, listener := thruApp(t)
	if _, err := app.MidiThruStart("Controller", false); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := app.Disconnect(); err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	if !listener.closed {
		t.Error("thru listener should close when the transport goes away")
	}
	if st := app.MidiThruStatus(); st.Active {
		t.Error("thru still active after disconnect")
	}
}

func TestMidiThruStart_PropagatesOpenerError(t *testing.T) {
	app, _, _, _ := thruApp(t)
	app.openThruVirtual = func(string, func([]byte)) (io.Closer, error) {
		return nil, errors.New("virtual ports unsupported")
	}
	if _, err := app.MidiThruStart("", true); err == nil {
		t.Fatal("expected opener error to propagate")
	}
	// A failed start must leave thru stopped so a retry can succeed.
	if st := app.MidiThruStatus(); st.Active {
		t.Error("thru should not be active after failed start")
	}
}

func TestClassifyNote(t *testing.T) {
	cases := []struct {
		msg        []byte
		wantOn     bool
		wantOff    bool
		wantKey    uint16
	}{
		{[]byte{0x90, 60, 100}, true, false, 60},
		{[]byte{0x90, 60, 0}, false, true, 60},    // running-status off
		{[]byte{0x80, 60, 64}, false, true, 60},
		{[]byte{0x95, 36, 100}, true, false, 5<<8 | 36}, // channel 5
		{[]byte{0xB0, 7, 100}, false, false, 0},   // CC — not a note
		{[]byte{0xE0, 0, 64}, false, false, 0},    // bend — not a note
		{[]byte{0x90, 60}, false, false, 0},       // truncated
	}
	for _, c := range cases {
		key, on, off := classifyNote(c.msg)
		if on != c.wantOn || off != c.wantOff {
			t.Errorf("classifyNote(% X) = on:%v off:%v, want on:%v off:%v",
				c.msg, on, off, c.wantOn, c.wantOff)
		}
		if (on || off) && key != c.wantKey {
			t.Errorf("classifyNote(% X) key = %d, want %d", c.msg, key, c.wantKey)
		}
	}
}
