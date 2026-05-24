package protocol

import (
	"encoding/json"
	"testing"

	"github.com/bivers/s950/internal/sysex"
)

// buildExampleProgramPayload constructs a PRGM payload matching dxzl's
// commented example in src/ProgramsForm.h: one program named "TONE PRGRM"
// with one keygroup covering MIDI keys 24..127 mapped to sample "TONE".
func buildExampleProgramPayload() []byte {
	out := make([]byte, ProgramHeaderSize)

	putDB := func(off int, v byte) {
		lo, hi := sysex.EncodeDB(v)
		out[off], out[off+1] = lo, hi
	}
	putDW := func(off int, v uint16) {
		b := sysex.EncodeDW(v)
		copy(out[off:off+4], b[:])
	}

	// --- Header ---
	name := sysex.EncodeName("TONE PRGRM")
	copy(out[pHdrName:pHdrName+20], name[:])
	// PrUndef1, PrUndef2, KeyTilt all zero (already)
	// PrUndef3 = 7e 00 44 01 per dxzl example
	out[pHdrUndef3], out[pHdrUndef3+1] = 0x7E, 0x00
	out[pHdrUndef3+2], out[pHdrUndef3+3] = 0x44, 0x01
	// PrUndef4 = 0
	// PosXfade = 0
	// PrReser1 = 255 (per dxzl)
	putDB(pHdrReser1, 255)
	// NumKgs = 1
	putDB(pHdrNumKgs, 1)
	// PrUndef5 = 0 DW
	// MidiPgm = 0
	// Enable midi pgm = 255
	putDB(pHdrEnableMP, 255)
	// PrReser2, 3, 4 = 0
	_ = putDW // keep helper used

	// --- Keygroup 1 ---
	kg := make([]byte, KeygroupSize)
	putDBkg := func(off int, v byte) {
		lo, hi := sysex.EncodeDB(v)
		kg[off], kg[off+1] = lo, hi
	}
	putDBkg(kgUMK, 127)
	putDBkg(kgLMK, 24)
	putDBkg(kgVST, 128)
	putDBkg(kgATK, 0)
	putDBkg(kgDCY, 80)
	putDBkg(kgSSTN, 99)
	putDBkg(kgRLSE, 30)
	putDBkg(kgFVI, 10)
	putDBkg(kgFKI, 50)
	putDBkg(kgAVI, 0)
	putDBkg(kgRVI, 0)
	putDBkg(kgLVI, 30)
	putDBkg(kgPVI, 0)
	putDBkg(kgPAO, 0)
	putDBkg(kgPST, 99)
	putDBkg(kgVBDLY, 64)
	putDBkg(kgVBRATE, 42)
	putDBkg(kgVBDPTH, 0)
	putDBkg(kgKBITS, 4)
	putDBkg(kgOPVOICE, 255)
	putDBkg(kgKMDCHN, 0)
	putDBkg(kgAFDI, 0)
	putDBkg(kgMWDI, 50)
	putDBkg(kgVCFAMNT, 0)
	soft := sysex.EncodeName("TONE")
	copy(kg[kgNAMEFS:kgNAMEFS+20], soft[:])
	putDBkg(kgVCFAK, 20)
	putDBkg(kgVCFDY, 20)
	putDBkg(kgVCFST, 20)
	putDBkg(kgVCFRL, 20)
	putDBkg(kgVTMX, 64)
	// kgTROFFS = 0 (DW signed)
	putDBkg(kgFLTFS, 99)
	putDBkg(kgLOUDFS, 0)
	loud := sysex.EncodeName("2 SAMPLE")
	copy(kg[kgNAMESS:kgNAMESS+20], loud[:])
	// kgTROFSS = 0
	putDBkg(kgFLTSS, 99)
	putDBkg(kgLOUDSS, 0)

	out = append(out, kg...)
	return out
}

func TestParseProgram_DxzlExample(t *testing.T) {
	payload := buildExampleProgramPayload()
	if len(payload) != ProgramHeaderSize+KeygroupSize {
		t.Fatalf("payload size = %d, want %d", len(payload), ProgramHeaderSize+KeygroupSize)
	}
	p, err := ParseProgram(payload)
	if err != nil {
		t.Fatalf("ParseProgram: %v", err)
	}
	if p.Name != "TONE PRGRM" {
		t.Errorf("Name = %q, want %q", p.Name, "TONE PRGRM")
	}
	if p.KeyTilt != 0 {
		t.Errorf("KeyTilt = %d, want 0", p.KeyTilt)
	}
	if p.PositionalXFade {
		t.Error("PositionalXFade should be false")
	}
	if p.NumKeygroups != 1 {
		t.Errorf("NumKeygroups = %d, want 1", p.NumKeygroups)
	}
	if !p.EnableMidiProgram {
		t.Error("EnableMidiProgram should be true (255 in raw)")
	}
	if len(p.Keygroups) != 1 {
		t.Fatalf("Keygroups len = %d, want 1", len(p.Keygroups))
	}
	kg := p.Keygroups[0]
	if kg.LowerKey != 24 || kg.UpperKey != 127 {
		t.Errorf("key range = %d..%d, want 24..127", kg.LowerKey, kg.UpperKey)
	}
	if kg.SoftSampleName != "TONE" {
		t.Errorf("SoftSampleName = %q, want %q", kg.SoftSampleName, "TONE")
	}
	if kg.LoudSampleName != "2 SAMPLE" {
		t.Errorf("LoudSampleName = %q, want %q", kg.LoudSampleName, "2 SAMPLE")
	}
	if kg.AttackTime != 0 || kg.DecayTime != 80 || kg.SustainLevel != 99 || kg.ReleaseTime != 30 {
		t.Errorf("ADSR = %d/%d/%d/%d, want 0/80/99/30",
			kg.AttackTime, kg.DecayTime, kg.SustainLevel, kg.ReleaseTime)
	}
	if kg.LfoBuildTime != 64 || kg.LfoRate != 42 {
		t.Errorf("LFO = build %d rate %d, want 64 42", kg.LfoBuildTime, kg.LfoRate)
	}
	if kg.VoiceOutAssign != 255 {
		t.Errorf("VoiceOutAssign = %d, want 255 (ALL)", kg.VoiceOutAssign)
	}
	if kg.SoftTransposeRaw != 0 {
		t.Errorf("SoftTransposeRaw = %d, want 0", kg.SoftTransposeRaw)
	}
}

func TestProgramEncodeRoundTrip(t *testing.T) {
	payload := buildExampleProgramPayload()
	p, err := ParseProgram(payload)
	if err != nil {
		t.Fatal(err)
	}
	// Mutate a few fields.
	p.Name = "MY KIT"
	p.Keygroups[0].SoftSampleName = "KICK"
	p.Keygroups[0].SoftTransposeRaw = -48 // -3 semitones
	p.Keygroups[0].LowerKey = 36
	p.Keygroups[0].UpperKey = 47

	out := p.EncodePayload()
	if len(out) != len(payload) {
		t.Fatalf("encoded size %d != original %d", len(out), len(payload))
	}

	// Re-parse and verify mutations stuck.
	q, err := ParseProgram(out)
	if err != nil {
		t.Fatal(err)
	}
	if q.Name != "MY KIT" {
		t.Errorf("Name didn't survive: got %q", q.Name)
	}
	if q.Keygroups[0].SoftSampleName != "KICK" {
		t.Errorf("SoftSampleName didn't survive: got %q", q.Keygroups[0].SoftSampleName)
	}
	if q.Keygroups[0].SoftTransposeRaw != -48 {
		t.Errorf("SoftTransposeRaw didn't survive: got %d", q.Keygroups[0].SoftTransposeRaw)
	}
	if q.Keygroups[0].LowerKey != 36 || q.Keygroups[0].UpperKey != 47 {
		t.Errorf("Key range didn't survive: got %d..%d", q.Keygroups[0].LowerKey, q.Keygroups[0].UpperKey)
	}
	// Untouched fields (e.g. DecayTime) should also round-trip.
	if q.Keygroups[0].DecayTime != 80 {
		t.Errorf("DecayTime mutated: got %d", q.Keygroups[0].DecayTime)
	}
}

func TestProgramJSONRoundTrip(t *testing.T) {
	payload := buildExampleProgramPayload()
	p, err := ParseProgram(payload)
	if err != nil {
		t.Fatal(err)
	}
	j := p.ToJSON()
	if j.Name != "TONE PRGRM" {
		t.Errorf("JSON Name = %q", j.Name)
	}
	// SoftTune as float semitones.
	if j.Keygroups[0].SoftTune != 0 {
		t.Errorf("JSON SoftTune = %v, want 0", j.Keygroups[0].SoftTune)
	}

	// Marshal + unmarshal through real JSON.
	buf, err := json.Marshal(&j)
	if err != nil {
		t.Fatal(err)
	}
	var j2 ProgramJSON
	if err := json.Unmarshal(buf, &j2); err != nil {
		t.Fatal(err)
	}
	p2 := &Program{}
	if err := p2.FromJSON(&j2); err != nil {
		t.Fatal(err)
	}

	// Re-encode and compare to the original payload.
	out := p2.EncodePayload()
	if len(out) != len(payload) {
		t.Fatalf("encoded size %d != original %d", len(out), len(payload))
	}
	for i := range payload {
		if out[i] != payload[i] {
			t.Fatalf("byte %d differs after JSON round-trip: got 0x%02X want 0x%02X",
				i, out[i], payload[i])
		}
	}
}

func TestProgramJSON_TuneEditAffectsTransposeRaw(t *testing.T) {
	payload := buildExampleProgramPayload()
	p, _ := ParseProgram(payload)

	j := p.ToJSON()
	// User edits the JSON: down one octave for the soft sample.
	j.Keygroups[0].SoftTune = -12.0
	// And a sub-semitone tune for the loud sample.
	j.Keygroups[0].LoudTune = 0.5

	p2 := &Program{}
	if err := p2.FromJSON(&j); err != nil {
		t.Fatal(err)
	}
	if got := p2.Keygroups[0].SoftTransposeRaw; got != -192 {
		t.Errorf("SoftTransposeRaw = %d, want -192 (= -12 * 16)", got)
	}
	if got := p2.Keygroups[0].LoudTransposeRaw; got != 8 {
		t.Errorf("LoudTransposeRaw = %d, want 8 (= 0.5 * 16)", got)
	}
}

func TestNewDefaultProgram_EncodeParseRoundTrip(t *testing.T) {
	p := NewDefaultProgram("DRUMKIT 1", 4)
	if p.NumKeygroups != 4 {
		t.Errorf("NumKeygroups = %d, want 4", p.NumKeygroups)
	}
	if len(p.Keygroups) != 4 {
		t.Fatalf("Keygroups len = %d, want 4", len(p.Keygroups))
	}

	// Encode → parse → all named fields match.
	payload := p.EncodePayload()
	if len(payload) != ProgramHeaderSize+4*KeygroupSize {
		t.Fatalf("payload size %d, want %d", len(payload), ProgramHeaderSize+4*KeygroupSize)
	}
	q, err := ParseProgram(payload)
	if err != nil {
		t.Fatalf("ParseProgram: %v", err)
	}
	if q.Name != "DRUMKIT 1" {
		t.Errorf("Name = %q", q.Name)
	}
	if q.NumKeygroups != 4 || len(q.Keygroups) != 4 {
		t.Errorf("keygroup count round-trip: %d / %d", q.NumKeygroups, len(q.Keygroups))
	}
	// Spot-check default values survived.
	if q.Keygroups[0].DecayTime != 80 || q.Keygroups[0].SustainLevel != 99 {
		t.Errorf("Default ADSR didn't survive: D=%d S=%d", q.Keygroups[0].DecayTime, q.Keygroups[0].SustainLevel)
	}
	if q.Keygroups[0].VoiceOutAssign != 255 {
		t.Errorf("VoiceOutAssign default = %d", q.Keygroups[0].VoiceOutAssign)
	}
	if !q.EnableMidiProgram {
		t.Error("EnableMidiProgram default should be true")
	}
}

func TestNewDefaultProgram_ClampsKeygroupCount(t *testing.T) {
	if p := NewDefaultProgram("", 0); p.NumKeygroups != 1 {
		t.Errorf("0 keygroups should clamp to 1, got %d", p.NumKeygroups)
	}
	if p := NewDefaultProgram("", 99); p.NumKeygroups != MaxKeygroups {
		t.Errorf("99 keygroups should clamp to %d, got %d", MaxKeygroups, p.NumKeygroups)
	}
}

func TestProgramJSON_SamplesManifest(t *testing.T) {
	// A kit JSON with both a samples manifest and keygroups should round-trip
	// through json.Marshal/Unmarshal preserving the SampleSpec entries.
	src := `{
  "name": "DRUM KIT",
  "key_tilt": 0,
  "positional_xfade": false,
  "num_keygroups": 1,
  "midi_program_number": 0,
  "enable_midi_program": true,
  "samples": [
    { "file": "kick.wav", "name": "KICK", "rate": "sp1200", "channel_mode": "left" },
    { "file": "snare.wav", "name": "SNARE", "tune": -3.5 },
    { "file": "hat.wav" }
  ],
  "keygroups": [],
  "_raw_header_hex": "` + hexAllZero(ProgramHeaderSize) + `"
}`
	var pj ProgramJSON
	if err := json.Unmarshal([]byte(src), &pj); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(pj.Samples) != 3 {
		t.Fatalf("Samples len = %d, want 3", len(pj.Samples))
	}
	if pj.Samples[0].File != "kick.wav" || pj.Samples[0].Name != "KICK" {
		t.Errorf("samples[0] = %+v", pj.Samples[0])
	}
	if pj.Samples[0].Rate != "sp1200" || pj.Samples[0].ChannelMode != "left" {
		t.Errorf("samples[0] options = %+v", pj.Samples[0])
	}
	if pj.Samples[1].Tune != -3.5 {
		t.Errorf("samples[1].Tune = %v, want -3.5", pj.Samples[1].Tune)
	}
	// samples[2] should have only File set — defaults for everything else.
	if pj.Samples[2].File != "hat.wav" || pj.Samples[2].Name != "" || pj.Samples[2].Rate != "" {
		t.Errorf("samples[2] = %+v", pj.Samples[2])
	}
}

func TestProgramJSON_SamplesOmittedWhenEmpty(t *testing.T) {
	// When a program has no samples manifest, the JSON should not include
	// an empty "samples": [] (uses omitempty).
	p := NewDefaultProgram("T", 1)
	j := p.ToJSON()
	buf, err := json.Marshal(&j)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(buf); contains(got, `"samples"`) {
		t.Errorf("expected no \"samples\" key in default JSON, got: %s", got)
	}
}

// hexAllZero returns a 2n-char hex string of zero bytes — used to satisfy
// the _raw_header_hex length requirement in tests that focus on other fields.
func hexAllZero(n int) string {
	out := make([]byte, n*2)
	for i := range out {
		out[i] = '0'
	}
	return string(out)
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestParseProgram_TooShortRejected(t *testing.T) {
	if _, err := ParseProgram(make([]byte, 100)); err == nil {
		t.Error("expected error on too-short payload")
	}
}

func TestParseProgram_BodyMisalignedRejected(t *testing.T) {
	// Header + 1 byte less than one keygroup → not a multiple of KeygroupSize.
	bad := make([]byte, ProgramHeaderSize+KeygroupSize-1)
	if _, err := ParseProgram(bad); err == nil {
		t.Error("expected error on misaligned body")
	}
}
