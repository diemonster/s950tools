package akaidisk

import (
	"encoding/binary"
	"strings"
	"testing"

	"github.com/bivers/s950/internal/protocol"
)

// Round-trip tests: build disk files with the builders, parse them
// back with the parsers, and compare against the originals. This is
// the contract that makes a Gotek image the app's patch format —
// what you export must load back identically.

func TestSampleFileRoundTrip(t *testing.T) {
	words := make([]uint16, 1001) // odd count exercises the pad path
	for i := range words {
		words[i] = uint16((i * 13) % 4096)
	}
	in := &protocol.SampleParams{
		Name: "ROUNDTRIP ", TotalWords: 1001, SampleRateHz: 40000,
		NominalPitch: 944, LoudOffset: -3, ReplayMode: 'A',
		Start: 10, End: 990, LoopLength: 500, Reversed: 'R', VelXFade: 255,
	}
	f, err := BuildSampleFile(in, words)
	if err != nil {
		t.Fatal(err)
	}
	out, gotWords, err := ParseSampleFile(f)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimRight(out.Name, " ") != "ROUNDTRIP" {
		t.Errorf("Name = %q", out.Name)
	}
	if out.TotalWords != 1001 || out.SampleRateHz != 40000 || out.NominalPitch != 944 {
		t.Errorf("numeric fields: %+v", out)
	}
	if out.LoudOffset != -3 {
		t.Errorf("LoudOffset = %d, want -3 (signed round-trip)", out.LoudOffset)
	}
	if out.ReplayMode != 'A' || out.Reversed != 'R' || out.VelXFade != 255 {
		t.Errorf("flags: mode %c rev %c vxf %d", out.ReplayMode, out.Reversed, out.VelXFade)
	}
	if out.Start != 10 || out.End != 990 || out.LoopLength != 500 {
		t.Errorf("markers: %d/%d/%d", out.Start, out.End, out.LoopLength)
	}
	if len(gotWords) != len(words) {
		t.Fatalf("words len %d, want %d", len(gotWords), len(words))
	}
	for i := range words {
		if gotWords[i] != words[i] {
			t.Fatalf("word %d: %#03x != %#03x", i, gotWords[i], words[i])
		}
	}
}

func TestProgramFileRoundTrip(t *testing.T) {
	in := protocol.NewDefaultProgram("RTPROG", 3)
	in.Keygroups[1].LowerKey = 48
	in.Keygroups[1].UpperKey = 60
	in.Keygroups[1].SoftSampleName = "RTKICK    "
	f, err := BuildProgramFile(in)
	if err != nil {
		t.Fatal(err)
	}
	out, err := ParseProgramFile(f)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimRight(out.Name, " ") != "RTPROG" {
		t.Errorf("Name = %q", out.Name)
	}
	if len(out.Keygroups) != 3 {
		t.Fatalf("keygroups = %d, want 3", len(out.Keygroups))
	}
	kg := out.Keygroups[1]
	if kg.LowerKey != 48 || kg.UpperKey != 60 {
		t.Errorf("kg1 range %d-%d, want 48-60", kg.LowerKey, kg.UpperKey)
	}
	if strings.TrimRight(kg.SoftSampleName, " ") != "RTKICK" {
		t.Errorf("kg1 soft sample = %q", kg.SoftSampleName)
	}
}

func TestOverallFileRoundTrip(t *testing.T) {
	in := &protocol.OverallSettings{
		ProgName: "RTVOL     ", BasicChannel: 3, OmniOn: true,
		MidiTxChannel: 5, BaudRate: 50000, PitchWheelRange: 2,
	}
	f, err := BuildOverallFile(in, true)
	if err != nil {
		t.Fatal(err)
	}
	out, err := ParseOverallFile(f)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimRight(out.ProgName, " ") != "RTVOL" {
		t.Errorf("ProgName = %q", out.ProgName)
	}
	if out.BasicChannel != 3 || !out.OmniOn || out.MidiTxChannel != 5 {
		t.Errorf("channels: %+v", out)
	}
	if out.BaudRate != 50000 || out.PitchWheelRange != 2 {
		t.Errorf("baud/pw: %+v", out)
	}
	// forceRS232 was applied at build time; the parse must see it.
	if out.ControllerSelect != 2 {
		t.Errorf("ControllerSelect = %d, want 2", out.ControllerSelect)
	}
}

func TestFullImagePatchRoundTrip(t *testing.T) {
	// The patch-format contract end-to-end: image with program +
	// samples + OVS, written to bytes, re-parsed, every file
	// recovered through the typed parsers.
	im := New()
	words := []uint16{0x800, 0x123, 0xFFF, 0x000, 0xABC}
	sp := &protocol.SampleParams{
		Name: "PATCHKICK ", TotalWords: 5, SampleRateHz: 26040,
		NominalPitch: 960, ReplayMode: 'O', End: 4, Reversed: 'N',
	}
	sf, _ := BuildSampleFile(sp, words)
	if _, err := im.AddFile("PATCHKICK", TypeSample, sf); err != nil {
		t.Fatal(err)
	}
	pf, _ := BuildProgramFile(protocol.NewDefaultProgram("PATCHPROG", 1))
	if _, err := im.AddFile("PATCHPROG", TypeProgram, pf); err != nil {
		t.Fatal(err)
	}
	of, _ := BuildOverallFile(&protocol.OverallSettings{ProgName: "PATCH     ", BaudRate: 50000}, true)
	if _, err := im.AddFile(OverallFileName, TypeOverall, of); err != nil {
		t.Fatal(err)
	}

	im2, err := Parse(im.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	var sawS, sawP, sawO bool
	for _, e := range im2.Entries() {
		f, err := im2.ReadFile(e)
		if err != nil {
			t.Fatalf("ReadFile %q: %v", e.Name, err)
		}
		switch e.Type {
		case TypeSample:
			sawS = true
			p, w, err := ParseSampleFile(f)
			if err != nil {
				t.Fatal(err)
			}
			if p.SampleRateHz != 26040 || len(w) != 5 || w[2] != 0xFFF {
				t.Errorf("sample mismatch: %+v words %v", p, w)
			}
		case TypeProgram:
			sawP = true
			p, err := ParseProgramFile(f)
			if err != nil {
				t.Fatal(err)
			}
			if len(p.Keygroups) != 1 {
				t.Errorf("keygroups = %d", len(p.Keygroups))
			}
		case TypeOverall:
			sawO = true
			o, err := ParseOverallFile(f)
			if err != nil {
				t.Fatal(err)
			}
			if o.ControllerSelect != 2 {
				t.Errorf("ctrl = %d", o.ControllerSelect)
			}
		}
	}
	if !sawS || !sawP || !sawO {
		t.Errorf("missing types: S=%v P=%v O=%v", sawS, sawP, sawO)
	}
}

func TestEntry_CompressedFlagFromOsver(t *testing.T) {
	// Synthesize a directory entry with a nonzero osver — the S900
	// compressed-sample marker — and confirm Entries surfaces it.
	im := New()
	if _, err := im.AddFile("COMPRESSED", TypeSample, make([]byte, 100)); err != nil {
		t.Fatal(err)
	}
	// osver lives at entry bytes 22..23.
	binary.LittleEndian.PutUint16(im.Bytes()[22:24], 7)
	e := im.Entries()[0]
	if !e.Compressed {
		t.Error("Compressed flag not set for nonzero osver")
	}
	// Programs with nonzero osver are NOT compressed samples.
	im2 := New()
	if _, err := im2.AddFile("PROG", TypeProgram, make([]byte, 38)); err != nil {
		t.Fatal(err)
	}
	binary.LittleEndian.PutUint16(im2.Bytes()[22:24], 7)
	if im2.Entries()[0].Compressed {
		t.Error("Compressed flag must be sample-only")
	}
}

func TestParseSampleFile_TooShort(t *testing.T) {
	if _, _, err := ParseSampleFile(make([]byte, 10)); err == nil {
		t.Fatal("expected error for truncated sample file")
	}
}

func TestParseProgramFile_BadSize(t *testing.T) {
	// 38 + 35 bytes — not a whole number of keygroups.
	if _, err := ParseProgramFile(make([]byte, 73)); err == nil {
		t.Fatal("expected error for non-integral keygroup count")
	}
}
