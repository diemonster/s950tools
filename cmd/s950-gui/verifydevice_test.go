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
	// Any inbound SysEx envelope counts as proof-of-life — Ping
	// deliberately doesn't validate the contents (see the comment
	// on Device.Ping). The shape here is the device-inquiry reply
	// from a non-AKAI device; even that is enough.
	reply := []byte{0xF0, 0x7E, 0x7F, 0xF7}
	app, _ := newAppWithFake(reply)
	if err := app.VerifyDevice(); err != nil {
		t.Fatalf("VerifyDevice should succeed when device replies; got %v", err)
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
