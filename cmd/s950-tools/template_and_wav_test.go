package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-audio/wav"

	"github.com/bivers/s950/internal/protocol"
	"github.com/bivers/s950/internal/sample"
)

// Tests for the two remaining commands we set out to cover after the
// orchestration extractions. Both are pure (no transport, no Wails):
//
//   runProgramTemplate — emits the default program JSON the CLI
//     suggests as a starter file. Catches regressions if
//     protocol.NewDefaultProgram or its JSON tags change shape.
//   writeSampleWAV    — encodes 12-bit S950 words as 16-bit mono
//     WAV via sample.SWtoPCM16. Distinct from the GUI's
//     writeSampleWav (which takes int16 PCM directly).

func TestRunProgramTemplate_ProducesParseableJSON_WithMatchingKeygroupCount(t *testing.T) {
	cases := []int{1, 4, 16, 31}
	for _, n := range cases {
		var buf bytes.Buffer
		if err := runProgramTemplate(&buf, "TESTPROG", n); err != nil {
			t.Errorf("runProgramTemplate(n=%d): %v", n, err)
			continue
		}
		var got protocol.ProgramJSON
		if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
			t.Errorf("emitted JSON not parseable (n=%d): %v\n%s", n, err, buf.String())
			continue
		}
		if got.Name != "TESTPROG" {
			t.Errorf("n=%d: Name = %q, want %q", n, got.Name, "TESTPROG")
		}
		if len(got.Keygroups) != n {
			t.Errorf("n=%d: len(Keygroups) = %d, want %d", n, len(got.Keygroups), n)
		}
		if int(got.NumKeygroups) != n {
			t.Errorf("n=%d: NumKeygroups = %d, want %d", n, got.NumKeygroups, n)
		}
	}
}

func TestRunProgramTemplate_RoundTripsThroughFromJSON(t *testing.T) {
	// The emitted template must be valid input for put-program — i.e.
	// FromJSON has to accept it. This guards against the template
	// drifting away from the put-program decoder (e.g. someone adds a
	// required field to KeygroupJSON without updating NewDefaultProgram).
	var buf bytes.Buffer
	if err := runProgramTemplate(&buf, "ROUNDTRP", 3); err != nil {
		t.Fatalf("runProgramTemplate: %v", err)
	}
	var pj protocol.ProgramJSON
	if err := json.Unmarshal(buf.Bytes(), &pj); err != nil {
		t.Fatalf("unmarshal emitted JSON: %v", err)
	}
	p := &protocol.Program{}
	if err := p.FromJSON(&pj); err != nil {
		t.Fatalf("FromJSON on emitted template: %v", err)
	}
	if p.Name != "ROUNDTRP" {
		t.Errorf("round-tripped Name = %q, want %q", p.Name, "ROUNDTRP")
	}
	if len(p.Keygroups) != 3 {
		t.Errorf("round-tripped keygroup count = %d, want 3", len(p.Keygroups))
	}
}

func TestRunProgramTemplate_IsIndentedJSON(t *testing.T) {
	// File the user is meant to hand-edit — must be indented, not
	// minified. The cobra wrapper relies on this for the on-disk file
	// to be readable.
	var buf bytes.Buffer
	if err := runProgramTemplate(&buf, "INDENT", 1); err != nil {
		t.Fatalf("runProgramTemplate: %v", err)
	}
	// Indented JSON has at least one newline + leading space pattern.
	if !bytes.Contains(buf.Bytes(), []byte("\n  ")) {
		t.Errorf("expected 2-space indented JSON, got:\n%s", buf.String())
	}
}

func TestWriteSampleWAV_RoundTripsThroughDecoder(t *testing.T) {
	// Encode a small known sequence of 12-bit S950 words, decode the
	// resulting WAV, and confirm:
	//   - sample rate and channel count match
	//   - each int16 sample matches sample.SWtoPCM16(w) for its source
	// This pins the device-words → host-PCM conversion used by both
	// `get-sample` and the GUI's Copy-from-S950 path.
	path := filepath.Join(t.TempDir(), "out.wav")
	words := []uint16{
		0x000, // most negative
		0x800, // midpoint (offset-binary "zero")
		0xFFF, // most positive
		0x400,
		0xC00,
		0xABC,
	}
	const rate uint32 = 26040
	if err := writeSampleWAV(path, words, rate); err != nil {
		t.Fatalf("writeSampleWAV: %v", err)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open written wav: %v", err)
	}
	defer f.Close()
	dec := wav.NewDecoder(f)
	if !dec.IsValidFile() {
		t.Fatal("written file is not a valid WAV")
	}
	buf, err := dec.FullPCMBuffer()
	if err != nil {
		t.Fatalf("decode wav: %v", err)
	}
	if buf.Format.SampleRate != int(rate) {
		t.Errorf("SampleRate = %d, want %d", buf.Format.SampleRate, rate)
	}
	if buf.Format.NumChannels != 1 {
		t.Errorf("NumChannels = %d, want 1", buf.Format.NumChannels)
	}
	if len(buf.Data) != len(words) {
		t.Fatalf("frame count = %d, want %d", len(buf.Data), len(words))
	}
	for i, w := range words {
		want := int(sample.SWtoPCM16(w))
		if buf.Data[i] != want {
			t.Errorf("frame[%d] for word %#03x = %d, want %d (= SWtoPCM16)",
				i, w, buf.Data[i], want)
		}
	}
}

func TestWriteSampleWAV_EmptyWordsProducesValidWAV(t *testing.T) {
	// Empty input is a degenerate but possible case — the device can
	// return a 0-word dump if a slot is empty. The encoder must not
	// crash and must produce a parseable (if empty) WAV.
	path := filepath.Join(t.TempDir(), "empty.wav")
	if err := writeSampleWAV(path, nil, 22050); err != nil {
		t.Fatalf("writeSampleWAV(nil): %v", err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	if !wav.NewDecoder(f).IsValidFile() {
		t.Error("empty-words WAV is not a valid file")
	}
}

func TestWriteSampleWAV_ErrorsOnUncreatablePath(t *testing.T) {
	// Path in a non-existent directory → file create fails. The cobra
	// wrapper relies on this error being surfaced rather than silently
	// swallowed (so the user sees why the dump didn't land on disk).
	bad := filepath.Join(t.TempDir(), "no", "such", "dir", "out.wav")
	if err := writeSampleWAV(bad, []uint16{0x800}, 22050); err == nil {
		t.Fatal("expected error writing to nonexistent dir")
	}
}
