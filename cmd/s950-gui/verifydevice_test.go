package main

import (
	"strings"
	"testing"
)

// VerifyDevice exists so the frontend can distinguish "serial port
// opened" from "sampler is actually listening on the wire" — the
// silent-success path on RS-232 where the cable opens fine even
// though the S950 is still in MIDI controller-select mode. The two
// tests below pin both branches of that decision so the connect-time
// modal in the topbar can't regress.

func TestVerifyDevice_Errors_WhenDeviceSilent(t *testing.T) {
	// fakePingTransport with reply=nil → RecvSysEx returns a synthetic
	// timeout. VerifyDevice must surface that as an error; the
	// connect() path on the frontend turns this into the
	// "S950 not responding" modal.
	app, _ := newAppWithFake(nil)
	err := app.VerifyDevice()
	if err == nil {
		t.Fatal("VerifyDevice should fail when no reply arrives")
	}
	if !strings.Contains(err.Error(), "no reply") {
		t.Errorf("error should mention 'no reply'; got %v", err)
	}
}

func TestVerifyDevice_Succeeds_WhenDeviceResponds(t *testing.T) {
	// Only an AKAI exclusive (F0 47 …) counts as the pong. The old
	// "any SysEx counts" contract was disproven by a hardware crash:
	// the S950 streams ACK handshakes (F0 7E 7F F7) during open-loop
	// dumps, and a stale ACK satisfying the next upload's ping let a
	// second dump start while the device was mid-bookkeeping.
	reply := []byte{0xF0, 0x47, 0x00, 0x0B, 0x40, 0x00, 0x00, 0xF7}
	app, _ := newAppWithFake(reply)
	if err := app.VerifyDevice(); err != nil {
		t.Fatalf("VerifyDevice should succeed on an AKAI reply; got %v", err)
	}
}

func TestVerifyDevice_Errors_WhenDisconnected(t *testing.T) {
	// Bare App with nil transport — VerifyDevice must report the same
	// "not connected" sentinel as every other lockDevice() caller,
	// so the frontend can tell connect-failed-at-open apart from
	// connect-succeeded-but-no-ping.
	app := &App{}
	err := app.VerifyDevice()
	if err == nil {
		t.Fatal("VerifyDevice should fail with no transport open")
	}
	if !strings.Contains(err.Error(), "not connected") {
		t.Errorf("error should mention 'not connected'; got %v", err)
	}
}
