package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bivers/s950/internal/device"
	"github.com/bivers/s950/internal/protocol"
)

// fakePingTransport satisfies transport.Transport. The reply field
// controls whether Ping (inside SendSample) sees a responsive
// device. Used to verify the pre-flight check rejects unresponsive
// transports before the long upload runs.
//
// Hardware-discovered regression we guard against here: cable on
// serial, controller-select on MIDI → no inbound traffic, no NAKs
// during upload, dialog reported success, sample never landed.
type fakePingTransport struct {
	sent  [][]byte
	reply []byte // nil = simulate the no-listener timeout case
}

func (f *fakePingTransport) Send(b []byte) error {
	cp := make([]byte, len(b))
	copy(cp, b)
	f.sent = append(f.sent, cp)
	return nil
}
func (f *fakePingTransport) RecvSysEx(_ time.Duration) ([]byte, error) {
	if f.reply == nil {
		return nil, errors.New("simulated timeout")
	}
	return f.reply, nil
}
func (f *fakePingTransport) Drain()          {}
func (f *fakePingTransport) Close() error    { return nil }
func (f *fakePingTransport) InName() string  { return "fake" }
func (f *fakePingTransport) OutName() string { return "fake" }

// newAppWithFake wires a fake transport into a bare App for the
// SendSample integration test. ctx left nil; emitSendProgress
// guards against that explicitly so event emission is a no-op
// rather than a log.Fatal call.
func newAppWithFake(reply []byte) (*App, *fakePingTransport) {
	t := &fakePingTransport{reply: reply}
	d := device.New(t, 0)
	app := &App{
		tport: t,
		dev:   d,
		kind:  "serial",
	}
	return app, t
}

func TestSendSample_FailsFast_WhenDeviceNotResponding(t *testing.T) {
	// Device doesn't respond to anything → Ping times out → SendSample
	// returns an error mentioning the controller-select hint. The
	// upload itself never runs (no SDATA on the wire).
	app, fake := newAppWithFake(nil)
	words := make([]uint16, 1000)
	err := app.SendSample(5, words, 22050, protocol.SampleParams{})
	if err == nil {
		t.Fatal("SendSample should fail when Ping reports no reply")
	}
	if !strings.Contains(err.Error(), "not responding") {
		t.Errorf("error should mention 'not responding'; got %v", err)
	}
	// Pre-flight RCAT went out, but the giant SDATA didn't. The
	// fake captures everything Send saw — we expect exactly one
	// message (the RCAT ping). Any more means SendSample ran the
	// upload despite the failed ping; any fewer means the ping
	// itself was skipped.
	if len(fake.sent) != 1 {
		t.Errorf("expected 1 wire message (RCAT ping only); got %d", len(fake.sent))
	}
}

func TestSendSample_ProceedsPastPreflight_WhenDeviceResponds(t *testing.T) {
	// Ping succeeds (any inbound envelope is enough), so SendSample
	// proceeds past the pre-flight and into PutSampleOpenLoop. We
	// don't validate the full upload here — too much fake plumbing
	// — but observing the SDATA bytes hit the wire is enough to
	// prove the pre-flight didn't block.
	reply := []byte{0xF0, 0x47, 0x00, 0x0B, 0x40, 0x00, 0x00, 0xF7} // AKAI catalog-shaped pong
	app, fake := newAppWithFake(reply)
	words := make([]uint16, 1000)
	_ = app.SendSample(5, words, 22050, protocol.SampleParams{})
	// Beyond the ping, we expect the SDATA envelope + an SPRM write.
	// At minimum: 2 envelopes after the ping (3 total).
	if len(fake.sent) < 3 {
		t.Errorf("expected ping + SDATA + SPRM (3+ messages); got %d", len(fake.sent))
	}
}
