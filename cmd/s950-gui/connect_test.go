package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/bivers/s950/internal/transport"
)

// Tests for Connect, ConnectSerial, ListPorts, Disconnect, Status,
// and ProbeForS950 — all of which used to call transport.Open*
// directly. They now route through openMIDI / openSerial /
// listSerialPorts seams on App, which makes them drivable from
// fakes here without a real serial port or MIDI driver.
//
// fakePingTransport lives in sendsample_test.go and already
// satisfies transport.Transport; we reuse it as the opener's
// return value so the connect flow can adopt a Device.

// withOpeners returns an App wired with controlled openers. Tests
// rarely care about every seam; supplying nil for unused ones is
// fine (the methods that hit them are simply not exercised).
func withOpeners(
	openMIDI func(transport.Options) (transport.Transport, error),
	openSerial func(transport.SerialOptions) (transport.Transport, error),
	listSerial func() ([]string, error),
) *App {
	return &App{
		openMIDI:        openMIDI,
		openSerial:      openSerial,
		listSerialPorts: listSerial,
	}
}

// staticSerialOpener returns an opener that hands back the supplied
// transport every time, recording the SerialOptions it was given.
func staticSerialOpener(t transport.Transport, opens *[]transport.SerialOptions) func(transport.SerialOptions) (transport.Transport, error) {
	return func(o transport.SerialOptions) (transport.Transport, error) {
		if opens != nil {
			*opens = append(*opens, o)
		}
		return t, nil
	}
}

func TestConnect_RejectsChannelOutOfRange(t *testing.T) {
	app := withOpeners(
		func(o transport.Options) (transport.Transport, error) {
			return &fakePingTransport{}, nil
		}, nil, nil)
	for _, ch := range []int{-1, 16, 99} {
		if err := app.Connect("", "", ch); err == nil {
			t.Errorf("Connect(channel=%d) should reject out-of-range", ch)
		}
	}
}

func TestConnect_AdoptsTransportAndStateOnSuccess(t *testing.T) {
	fake := &fakePingTransport{}
	var passed []transport.Options
	opener := func(o transport.Options) (transport.Transport, error) {
		passed = append(passed, o)
		return fake, nil
	}
	app := withOpeners(opener, nil, nil)

	if err := app.Connect("MyInputPort", "MyOutputPort", 7); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if len(passed) != 1 {
		t.Fatalf("openMIDI called %d times, want 1", len(passed))
	}
	if passed[0].In != "MyInputPort" || passed[0].Out != "MyOutputPort" {
		t.Errorf("opener got In=%q Out=%q, want MyInputPort/MyOutputPort", passed[0].In, passed[0].Out)
	}
	// adoptTransportLocked must have wired state — Status reflects it.
	s := app.Status()
	if !s.Connected || s.Kind != "midi" || s.Channel != 7 {
		t.Errorf("post-Connect Status = %+v, want connected=true kind=midi channel=7", s)
	}
	if app.dev == nil {
		t.Error("dev not initialised after Connect")
	}
}

func TestConnect_PropagatesOpenerError(t *testing.T) {
	opener := func(o transport.Options) (transport.Transport, error) {
		return nil, errors.New("simulated rtmidi failure")
	}
	app := withOpeners(opener, nil, nil)
	err := app.Connect("", "", 0)
	if err == nil {
		t.Fatal("expected error when opener fails")
	}
	if !strings.Contains(err.Error(), "simulated rtmidi failure") {
		t.Errorf("error should wrap opener message; got %v", err)
	}
	// On failure, state must remain disconnected — no half-open transport.
	if s := app.Status(); s.Connected {
		t.Errorf("Status connected=true after failed Connect; want false (%+v)", s)
	}
}

func TestConnect_ClosesPreviousTransport(t *testing.T) {
	// Two successive Connects: the first transport's Close must fire
	// before the second is adopted. Using a closeCounter fake to
	// observe.
	closes := 0
	fake1 := &closeRecorder{count: &closes}
	fake2 := &fakePingTransport{}
	calls := 0
	opener := func(o transport.Options) (transport.Transport, error) {
		calls++
		if calls == 1 {
			return fake1, nil
		}
		return fake2, nil
	}
	app := withOpeners(opener, nil, nil)

	if err := app.Connect("", "", 0); err != nil {
		t.Fatalf("first Connect: %v", err)
	}
	if err := app.Connect("", "", 0); err != nil {
		t.Fatalf("second Connect: %v", err)
	}
	if closes != 1 {
		t.Errorf("expected 1 close on prior transport, got %d", closes)
	}
}

func TestConnectSerial_RejectsEmptyPort(t *testing.T) {
	app := withOpeners(nil,
		func(o transport.SerialOptions) (transport.Transport, error) { return &fakePingTransport{}, nil },
		nil)
	if err := app.ConnectSerial("", 38400); err == nil {
		t.Fatal("ConnectSerial should reject empty port")
	}
}

func TestConnectSerial_DefaultsZeroBaudTo38400(t *testing.T) {
	var captured []transport.SerialOptions
	app := withOpeners(nil, staticSerialOpener(&fakePingTransport{}, &captured), nil)
	if err := app.ConnectSerial("/dev/fake", 0); err != nil {
		t.Fatalf("ConnectSerial: %v", err)
	}
	if len(captured) != 1 {
		t.Fatalf("openSerial calls = %d, want 1", len(captured))
	}
	if captured[0].Baud != 38400 {
		t.Errorf("opener got Baud=%d, want 38400 (default for baud<=0)", captured[0].Baud)
	}
	s := app.Status()
	if s.Kind != "serial" || s.Baud != 38400 {
		t.Errorf("Status after serial Connect = %+v, want kind=serial baud=38400", s)
	}
}

func TestConnectSerial_RespectsExplicitBaud(t *testing.T) {
	var captured []transport.SerialOptions
	app := withOpeners(nil, staticSerialOpener(&fakePingTransport{}, &captured), nil)
	if err := app.ConnectSerial("/dev/fake", 76800); err != nil {
		t.Fatalf("ConnectSerial: %v", err)
	}
	if captured[0].Baud != 76800 {
		t.Errorf("opener got Baud=%d, want 76800", captured[0].Baud)
	}
	if app.Status().Baud != 76800 {
		t.Errorf("Status baud = %d, want 76800", app.Status().Baud)
	}
}

func TestDisconnect_IsIdempotent(t *testing.T) {
	app := withOpeners(nil, nil, nil)
	// First Disconnect on a never-connected App must be a no-op.
	if err := app.Disconnect(); err != nil {
		t.Errorf("first Disconnect on disconnected App: %v", err)
	}
	if err := app.Disconnect(); err != nil {
		t.Errorf("second Disconnect: %v", err)
	}
}

func TestDisconnect_ClosesAndClearsState(t *testing.T) {
	closes := 0
	app := withOpeners(
		func(o transport.Options) (transport.Transport, error) {
			return &closeRecorder{count: &closes}, nil
		}, nil, nil)
	if err := app.Connect("", "", 3); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := app.Disconnect(); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	if closes != 1 {
		t.Errorf("expected 1 close, got %d", closes)
	}
	s := app.Status()
	if s.Connected {
		t.Errorf("Status connected=true post-Disconnect; want false (%+v)", s)
	}
	if app.dev != nil {
		t.Error("dev should be nil post-Disconnect")
	}
}

func TestStatus_DisconnectedSentinel(t *testing.T) {
	app := withOpeners(nil, nil, nil)
	s := app.Status()
	if s.Connected {
		t.Errorf("bare App should report Connected=false, got %+v", s)
	}
}

func TestListPorts_BuildsSerialEntriesFromSeam(t *testing.T) {
	// Without a wails app + rtmidi backend transport.ListPorts itself
	// is heavy — this test focuses on the serial seam, which is the
	// part the topbar uses for the auto-discovery flow. If the MIDI
	// enumerate succeeds in CI's MIDI-less env we still get a clean
	// PortList; if it fails the test is informational only.
	listSerial := func() ([]string, error) {
		return []string{"/dev/fake1", "/dev/fake2"}, nil
	}
	app := withOpeners(nil, nil, listSerial)
	got, err := app.ListPorts()
	if err != nil {
		t.Skipf("ListPorts MIDI side failed in this env (expected on headless CI): %v", err)
	}
	if len(got.Serial) != 2 {
		t.Fatalf("Serial list = %d entries, want 2 (%v)", len(got.Serial), got.Serial)
	}
	if got.Serial[0].Name != "/dev/fake1" || got.Serial[1].Name != "/dev/fake2" {
		t.Errorf("Serial entries = %+v", got.Serial)
	}
}

func TestProbeForS950_NoUSBSerialPorts_Errors(t *testing.T) {
	// listSerial returns only non-USB-serial-looking names. The
	// filter strips everything, so the probe never opens anything
	// and the user gets the "plug in your S950 cable" error.
	listSerial := func() ([]string, error) {
		return []string{"/dev/cu.Bluetooth-Incoming-Port"}, nil
	}
	app := withOpeners(nil,
		func(o transport.SerialOptions) (transport.Transport, error) {
			t.Fatal("openSerial must not be called when no USB-serial candidates exist")
			return nil, nil
		},
		listSerial)
	_, err := app.ProbeForS950()
	if err == nil {
		t.Fatal("expected error when no USB-serial ports available")
	}
	if !strings.Contains(err.Error(), "no USB-serial ports") {
		t.Errorf("error should mention 'no USB-serial ports', got %v", err)
	}
}

func TestProbeForS950_NoMatch_ReportsTriedList(t *testing.T) {
	// Every probe attempt sees a fake that timeouts (reply == nil).
	// Errors must enumerate the (port, baud) pairs tried so the user
	// can see which candidates were inspected.
	silent := &fakePingTransport{reply: nil}
	app := withOpeners(nil,
		staticSerialOpener(silent, nil),
		usbSerialList,
	)
	_, err := app.ProbeForS950()
	if err == nil {
		t.Fatal("expected error when no probe matches")
	}
	if !strings.Contains(err.Error(), "no S950 found") {
		t.Errorf("error should mention 'no S950 found', got %v", err)
	}
	// The error string lists at least one port @ baud pair.
	if !strings.Contains(err.Error(), "@") {
		t.Errorf("error should include 'port @ baud' attempt list, got %v", err)
	}
}

func TestProbeForS950_MatchesOnAkaiReply(t *testing.T) {
	// First probe attempt returns an AKAI-shaped reply (F0 47 …).
	// ProbeForS950 must return without trying further bauds/ports.
	akai := []byte{0xF0, 0x47, 0x00, 0xF7}
	hit := &fakePingTransport{reply: akai}
	var seen []transport.SerialOptions
	app := withOpeners(nil,
		staticSerialOpener(hit, &seen),
		usbSerialList,
	)
	got, err := app.ProbeForS950()
	if err != nil {
		t.Fatalf("ProbeForS950: %v", err)
	}
	if got == nil {
		t.Fatal("expected ProbeResult, got nil")
	}
	if seen[0].Port == "" {
		t.Errorf("opener received empty port option")
	}
	// First baud tried is the fastest from probeBauds (50000).
	if seen[0].Baud != probeBauds[0] {
		t.Errorf("first probe baud = %d, want %d", seen[0].Baud, probeBauds[0])
	}
	if got.Baud != seen[0].Baud {
		t.Errorf("returned Baud = %d, want %d (= first attempted)", got.Baud, seen[0].Baud)
	}
}

func TestProbeForS950_RejectsNonAkaiReply(t *testing.T) {
	// Some other device on the bus replies, but not with F0 47. The
	// probe must treat that as a non-match and keep going — the
	// regression we're guarding against is a dev board interpreting
	// inbound SysEx as terminal data.
	notAkai := &fakePingTransport{reply: []byte{0xF0, 0x7E, 0x7F, 0xF7}}
	app := withOpeners(nil,
		staticSerialOpener(notAkai, nil),
		usbSerialList,
	)
	_, err := app.ProbeForS950()
	if err == nil {
		t.Fatal("non-AKAI reply must not count as a probe match")
	}
}

// usbSerialList returns one entry that passes filterUSBSerialPorts
// on the current host OS. Keeps the probe tests OS-portable.
func usbSerialList() ([]string, error) {
	for _, n := range []string{"/dev/cu.usbserial-TEST", "/dev/ttyUSB0", "COM3"} {
		if isLikelyUSBSerial(n) {
			return []string{n}, nil
		}
	}
	return nil, nil
}

// closeRecorder is a transport.Transport that bumps *count on Close.
// Used to verify Disconnect / Connect-over-Connect close the prior
// transport.
type closeRecorder struct {
	fakePingTransport
	count *int
}

func (c *closeRecorder) Close() error {
	*c.count++
	return nil
}
