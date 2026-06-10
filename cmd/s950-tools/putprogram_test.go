package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/bivers/s950/internal/device"
	"github.com/bivers/s950/internal/protocol"
)

// zeroHex returns a hex string encoding `n` zero bytes — used to
// satisfy ProgramJSON's RawHeaderHex and KeygroupJSON's RawBytesHex
// length checks for synthetic test programs. The on-device decode
// only requires the size match; field overrides win on encode.
func zeroHex(n int) string {
	return strings.Repeat("00", n)
}

// Tests for resolveSampleName and runPutProgram. The runPutProgram
// tests use the same queuedReplyTransport + emptyCATEnvelope +
// makeUploadTestWAV helpers as uploadsample_test.go (same package).
// SkipVerify=true throughout — synthesising a PRGM reply for the
// post-write verify is heavy; the wire-trace assertions already pin
// the orchestration.

func TestResolveSampleName_ExplicitNameWins(t *testing.T) {
	got := resolveSampleName(protocol.SampleSpec{
		Name: "EXPLICIT",
		File: "/some/path/kick.wav",
	})
	if got != "EXPLICIT" {
		t.Errorf("got %q, want %q", got, "EXPLICIT")
	}
}

func TestResolveSampleName_DerivesFromFile(t *testing.T) {
	cases := []struct {
		file string
		want string
	}{
		{"/path/kick.wav", "KICK"},
		{"snare.aiff", "SNARE"},
		{"/x/SUPERLONGSAMPLE.wav", "SUPERLONGS"}, // truncated to 10
		{"a.b.c.wav", "A.B.C"},                   // only last ext stripped
		{"plain", "PLAIN"},                       // no extension is fine
	}
	for _, c := range cases {
		got := resolveSampleName(protocol.SampleSpec{File: c.file})
		if got != c.want {
			t.Errorf("resolveSampleName(file=%q) = %q, want %q", c.file, got, c.want)
		}
	}
}

func TestResolveSampleName_EmptyNameAndEmptyFile(t *testing.T) {
	// Both fields blank — the function still returns a (possibly
	// empty) string rather than panicking. Pin the no-crash contract.
	if got := resolveSampleName(protocol.SampleSpec{}); got != "" {
		t.Errorf("empty spec should yield empty string, got %q", got)
	}
}

// minimalProgramJSON returns a 1-keygroup ProgramJSON the put-program
// flow can decode + send. No Samples manifest — the empty-manifest
// branch exercises the "just write the program" code path. The
// RawHeaderHex / RawBytesHex padding satisfies FromJSON's length
// checks (it requires exact byte counts for the round-trip-safe
// preservation of undocumented bytes).
func minimalProgramJSON(name string) *protocol.ProgramJSON {
	return &protocol.ProgramJSON{
		Name:         name,
		NumKeygroups: 1,
		RawHeaderHex: zeroHex(protocol.ProgramHeaderSize),
		Keygroups: []protocol.KeygroupJSON{
			{
				LowerKey:       24,
				UpperKey:       127,
				SoftSampleName: "TONE",
				LoudSampleName: "TONE",
				RawBytesHex:    zeroHex(protocol.KeygroupSize),
			},
		},
	}
}

func TestRunPutProgram_EmptyManifest_OnlySendsProgram(t *testing.T) {
	// No samples → no Catalog read, no SDATA. CollectNAKs polls but
	// the queue is empty (immediate timeout). SkipVerify=true skips
	// the post-write GetProgram. The fake should see exactly one
	// outbound envelope: the PRGM write.
	pj := minimalProgramJSON("EMPTYMFST")
	fake := &queuedReplyTransport{}
	d := device.New(fake, 0)

	var status bytes.Buffer
	if err := runPutProgram(&status, d, pj, putProgramOpts{
		Slot:       3,
		SkipVerify: true,
	}); err != nil {
		t.Fatalf("runPutProgram: %v", err)
	}
	if len(fake.sent) != 1 {
		t.Fatalf("expected 1 send (PRGM write), got %d", len(fake.sent))
	}
	akai, err := protocol.ParseAkai(fake.sent[0])
	if err != nil {
		t.Fatalf("PRGM envelope not AKAI-parseable: %v", err)
	}
	if akai.Function != protocol.FuncPRGM {
		t.Errorf("function = %d, want FuncPRGM", akai.Function)
	}
	if akai.Num != 3 {
		t.Errorf("written to slot %d, want 3", akai.Num)
	}
	if !strings.Contains(status.String(), "wrote program") {
		t.Errorf("status missing 'wrote program', got %q", status.String())
	}
}

func TestRunPutProgram_SkipsSampleAlreadyOnDevice(t *testing.T) {
	// Manifest has one entry named "KICK"; the catalog reply seeds
	// the same name. ForceSamples=false → upload skipped. The wire
	// trace must be exactly [RCAT (catalog), PRGM (program)] — no
	// SDATA between them.
	pj := minimalProgramJSON("SKIPTEST")
	pj.Samples = []protocol.SampleSpec{
		{File: "kick.wav", Name: "KICK"},
	}

	// One CAT reply with a single S-typed entry "KICK".
	catalogPayload := buildCatalogEntry('S', 0, "KICK")
	cat := protocol.BuildAkaiData(0, protocol.FuncCAT, 0, catalogPayload)
	fake := &queuedReplyTransport{replies: [][]byte{cat}}
	d := device.New(fake, 0)

	var status bytes.Buffer
	err := runPutProgram(&status, d, pj, putProgramOpts{
		Slot:         5,
		SkipVerify:   true,
		ForceSamples: false,
	})
	if err != nil {
		t.Fatalf("runPutProgram: %v", err)
	}
	if len(fake.sent) != 2 {
		t.Fatalf("expected 2 sends (RCAT + PRGM), got %d (%v)", len(fake.sent), fake.sent)
	}
	// Send[0] = RCAT (catalog request). Send[1] = PRGM write.
	if fake.sent[0][3] != protocol.FuncRCAT {
		t.Errorf("first send func = %#x, want FuncRCAT", fake.sent[0][3])
	}
	if fake.sent[1][3] != protocol.FuncPRGM {
		t.Errorf("second send func = %#x, want FuncPRGM", fake.sent[1][3])
	}
	if !strings.Contains(status.String(), "already on device, skipping") {
		t.Errorf("expected skip status line, got %q", status.String())
	}
}

func TestRunPutProgram_NAKDuringProgramWriteIsError(t *testing.T) {
	// After SetProgram, CollectNAKs polls inbound for 500 ms. Seed a
	// NAK envelope so it counts → runPutProgram must error out with
	// the user-facing "may not have stored cleanly" message.
	pj := minimalProgramJSON("NAKTEST")
	nak := []byte{0xF0, 0x7E, 0x7E, 0xF7} // CodeNAKS = 0x7E
	fake := &queuedReplyTransport{replies: [][]byte{nak}}
	d := device.New(fake, 0)

	var status bytes.Buffer
	err := runPutProgram(&status, d, pj, putProgramOpts{
		Slot:       1,
		SkipVerify: true,
	})
	if err == nil {
		t.Fatal("expected NAK error from program write")
	}
	if !strings.Contains(err.Error(), "may not have stored cleanly") {
		t.Errorf("error should mention 'may not have stored cleanly', got %v", err)
	}
}
