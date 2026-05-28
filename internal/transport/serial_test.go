package transport

import (
	"bytes"
	"testing"
)

// sysexAssembler is the pure byte-stream parser the serial reader
// uses to assemble F0..F7 envelopes. These tests exercise it
// without spinning up a serial.Port — the same machinery that
// drives every RS-232 round-trip in production, including the
// catalog read, SPRM round-trips, and streaming sample dumps.

func feedAll(a *sysexAssembler, in []byte) [][]byte {
	var out [][]byte
	for _, b := range in {
		if env := a.Step(b); env != nil {
			out = append(out, env)
		}
	}
	return out
}

func TestSysexAssembler_SingleEnvelopeInOnePass(t *testing.T) {
	var a sysexAssembler
	in := []byte{0xF0, 0x47, 0x00, 0x03, 0x40, 0x00, 0x00, 0xF7}
	envs := feedAll(&a, in)
	if len(envs) != 1 {
		t.Fatalf("got %d envelopes, want 1", len(envs))
	}
	if !bytes.Equal(envs[0], in) {
		t.Errorf("envelope = % X, want % X", envs[0], in)
	}
}

func TestSysexAssembler_MultipleEnvelopesInOneBatch(t *testing.T) {
	// Two back-to-back envelopes — typical when the device replies
	// to a request quickly and the OS coalesces both into a single
	// read. The assembler must split them at the F7 → F0 boundary.
	var a sysexAssembler
	in := []byte{
		0xF0, 0x7E, 0x7F, 0xF7, // ACK
		0xF0, 0x47, 0x00, 0x0B, 0x40, 0x00, 0x00, 0x42, 0xF7, // tiny CAT-like reply
	}
	envs := feedAll(&a, in)
	if len(envs) != 2 {
		t.Fatalf("got %d envelopes, want 2", len(envs))
	}
	if !bytes.Equal(envs[0], in[:4]) {
		t.Errorf("env[0] = % X, want % X", envs[0], in[:4])
	}
	if !bytes.Equal(envs[1], in[4:]) {
		t.Errorf("env[1] = % X, want % X", envs[1], in[4:])
	}
}

func TestSysexAssembler_EnvelopeSplitAcrossMultipleSteps(t *testing.T) {
	// One envelope arriving as several OS reads. Each Step call
	// receives one byte; the envelope only resolves when F7 lands.
	var a sysexAssembler
	in := []byte{0xF0, 0x7E, 0x01, 0x05, 0x06, 0xF7}
	var envs [][]byte
	for _, b := range in {
		if env := a.Step(b); env != nil {
			envs = append(envs, env)
		}
	}
	if len(envs) != 1 {
		t.Fatalf("got %d envelopes, want 1", len(envs))
	}
	if !bytes.Equal(envs[0], in) {
		t.Errorf("envelope = % X, want % X", envs[0], in)
	}
}

func TestSysexAssembler_DropsBytesOutsideEnvelope(t *testing.T) {
	// Stray bytes between envelopes (running-status leakage etc.)
	// must be discarded — the next F0 should still start a clean
	// envelope. Defensive against UART noise on long cables.
	var a sysexAssembler
	in := []byte{
		0x90, 0x40, 0x7F, // stray MIDI Note On (not in a SysEx)
		0xF0, 0x47, 0x42, 0xF7,
	}
	envs := feedAll(&a, in)
	if len(envs) != 1 {
		t.Fatalf("got %d envelopes, want 1", len(envs))
	}
	want := []byte{0xF0, 0x47, 0x42, 0xF7}
	if !bytes.Equal(envs[0], want) {
		t.Errorf("envelope = % X, want % X", envs[0], want)
	}
}

func TestSysexAssembler_MidEnvelopeF0DropsPartial(t *testing.T) {
	// A second F0 inside an envelope means the device aborted and
	// restarted. The assembler must drop the partial and begin a
	// fresh envelope — otherwise corrupted bytes from the abort
	// would land in the next valid message.
	var a sysexAssembler
	in := []byte{
		0xF0, 0x47, 0x00, 0x99, // partial — never sees F7
		0xF0, 0x47, 0x42, 0xF7, // proper envelope
	}
	envs := feedAll(&a, in)
	if len(envs) != 1 {
		t.Fatalf("got %d envelopes, want 1 (partial must be dropped)", len(envs))
	}
	want := []byte{0xF0, 0x47, 0x42, 0xF7}
	if !bytes.Equal(envs[0], want) {
		t.Errorf("envelope = % X, want % X", envs[0], want)
	}
}

func TestSysexAssembler_NoOutputForPartial(t *testing.T) {
	// An envelope that never sees F7 must not be returned. The
	// reader's caller relies on Step's nil to mean "nothing yet";
	// returning a partial would deliver a corrupted SPRM/CAT to
	// the device layer.
	var a sysexAssembler
	in := []byte{0xF0, 0x47, 0x00, 0x03, 0x40, 0x00, 0x00}
	envs := feedAll(&a, in)
	if len(envs) != 0 {
		t.Errorf("got %d envelopes, want 0 (no F7)", len(envs))
	}
}

func TestSysexAssembler_EnvelopeBufferIsCopied(t *testing.T) {
	// The returned envelope must not alias the assembler's
	// internal buffer — the assembler may overwrite it on the
	// next envelope, which would silently mutate any envelope
	// already in flight to a consumer.
	var a sysexAssembler
	env1 := feedAll(&a, []byte{0xF0, 0x11, 0xF7})
	env2 := feedAll(&a, []byte{0xF0, 0x22, 0xF7})
	if len(env1) != 1 || len(env2) != 1 {
		t.Fatalf("env counts: %d / %d", len(env1), len(env2))
	}
	if env1[0][1] == env2[0][1] {
		t.Errorf("envelopes alias each other: env1=% X env2=% X", env1[0], env2[0])
	}
}

func TestSysexAssembler_HandlesLargeEnvelope(t *testing.T) {
	// 165-word sample dump body is ~340 bytes (header + 3 blocks +
	// F7). Sanity that the accumulator doesn't truncate or alloc-
	// limit. Build a synthetic large envelope and verify size.
	const bodyBytes = 350
	in := make([]byte, 0, bodyBytes+2)
	in = append(in, 0xF0)
	for i := 0; i < bodyBytes; i++ {
		in = append(in, byte(i&0x7F))
	}
	in = append(in, 0xF7)

	var a sysexAssembler
	envs := feedAll(&a, in)
	if len(envs) != 1 {
		t.Fatalf("got %d envelopes, want 1", len(envs))
	}
	if len(envs[0]) != bodyBytes+2 {
		t.Errorf("envelope len = %d, want %d", len(envs[0]), bodyBytes+2)
	}
}
