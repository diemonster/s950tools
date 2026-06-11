package main

import (
	"bytes"
	"strings"
	"testing"
)

// ActivateProgram is fire-and-forget by hardware design (the S950
// can't report its active program), so the only things worth pinning
// down are the wire bytes and the input validation — a malformed PC
// would silently select the wrong program with no error path.

func TestActivateProgram_SendsProgramChangeBytes(t *testing.T) {
	app, fake := newAppWithFake(nil)
	if err := app.ActivateProgram(5, 2); err != nil {
		t.Fatalf("ActivateProgram: %v", err)
	}
	want := []byte{0xC2, 0x05}
	if len(fake.sent) != 1 || !bytes.Equal(fake.sent[0], want) {
		t.Errorf("sent = %x, want %x", fake.sent, want)
	}
}

func TestActivateProgram_MasksChannelToFourBits(t *testing.T) {
	app, fake := newAppWithFake(nil)
	if err := app.ActivateProgram(0, 18); err != nil {
		t.Fatalf("ActivateProgram: %v", err)
	}
	// channel 18 & 0x0F = 2 — out-of-range channels degrade to a
	// valid status byte instead of corrupting the message type.
	if got := fake.sent[0][0]; got != 0xC2 {
		t.Errorf("status byte = %#x, want 0xC2", got)
	}
}

func TestActivateProgram_RejectsOutOfRangeProgram(t *testing.T) {
	app, fake := newAppWithFake(nil)
	for _, n := range []int{-1, 128} {
		if err := app.ActivateProgram(n, 0); err == nil {
			t.Errorf("ActivateProgram(%d) should fail", n)
		}
	}
	if len(fake.sent) != 0 {
		t.Errorf("nothing should reach the wire on validation failure, sent %x", fake.sent)
	}
}

func TestActivateProgram_FailsWhenNotConnected(t *testing.T) {
	app := &App{}
	err := app.ActivateProgram(0, 0)
	if err == nil || !strings.Contains(err.Error(), "not connected") {
		t.Errorf("want 'not connected' error, got %v", err)
	}
}
