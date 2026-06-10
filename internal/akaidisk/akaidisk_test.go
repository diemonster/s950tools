package akaidisk

import (
	"bytes"
	"errors"
	"testing"

	"github.com/bivers/s950/internal/protocol"
)

// ---------- 12-bit packing ----------

func TestPackUnpackRoundTrip(t *testing.T) {
	cases := [][]uint16{
		{0x800},                      // single sample (odd count, pad path)
		{0x000, 0xFFF},               // extremes
		{0x800, 0x800, 0x800},        // odd count silence
		{0x123, 0xABC, 0xDEF, 0x456}, // even count
	}
	for _, words := range cases {
		packed := PackSamples(words)
		wantLen := 3 * ((len(words) + 1) / 2)
		if len(packed) != wantLen {
			t.Errorf("PackSamples(%d words) = %d bytes, want %d", len(words), len(packed), wantLen)
		}
		got, err := UnpackSamples(packed, len(words))
		if err != nil {
			t.Fatalf("UnpackSamples: %v", err)
		}
		for i := range words {
			if got[i] != words[i] {
				t.Errorf("words[%d]: got %#03x, want %#03x (packed % X)", i, got[i], words[i], packed)
			}
		}
	}
}

func TestPackSamples_LayoutMatchesS900Format(t *testing.T) {
	// Pin the exact byte layout for a known input so a refactor
	// can't silently swap nibbles. words: [0x900, 0xA00] →
	// pcm16: (0x900-0x800)<<4 = 0x1000; (0xA00-0x800)<<4 = 0x2000.
	// P=1. Layout: [sharednib, hiA, hiB] = [0x00, 0x10, 0x20].
	packed := PackSamples([]uint16{0x900, 0xA00})
	want := []byte{0x00, 0x10, 0x20}
	if !bytes.Equal(packed, want) {
		t.Errorf("packed = % X, want % X", packed, want)
	}
	// Nibble carriers: words yielding pcm16 with bits 7..4 set.
	// word 0x801 → pcm 0x0010 (nibble 1); word 0x802 → pcm 0x0020
	// (nibble 2). Shared byte = 0x12.
	packed = PackSamples([]uint16{0x801, 0x802})
	want = []byte{0x12, 0x00, 0x00}
	if !bytes.Equal(packed, want) {
		t.Errorf("nibble layout = % X, want % X", packed, want)
	}
}

func TestPackSamples_Empty(t *testing.T) {
	if got := PackSamples(nil); got != nil {
		t.Errorf("PackSamples(nil) = % X, want nil", got)
	}
}

// ---------- DB decoding ----------

func TestDecodeDBPayload(t *testing.T) {
	// 0xAB = lo 0x2B + hi 1; 0x7F = lo 0x7F + hi 0.
	wire := []byte{0x2B, 0x01, 0x7F, 0x00}
	got := DecodeDBPayload(wire)
	if !bytes.Equal(got, []byte{0xAB, 0x7F}) {
		t.Errorf("DecodeDBPayload = % X, want AB 7F", got)
	}
}

// ---------- name sanitization ----------

func TestSanitizeName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"kick", "KICK"},
		{"7-Audio 1", "7-AUDIO 1"},
		{"way too long name", "WAY TOO LO"},
		{"weird/char*", "WEIRD.CHAR"},
		{"  trimmed  ", "TRIMMED"},
	}
	for _, c := range cases {
		if got := SanitizeName(c.in); got != c.want {
			t.Errorf("SanitizeName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// ---------- image round-trip ----------

func TestImageRoundTrip(t *testing.T) {
	im := New()
	// A multi-block file (3000 bytes → 3 blocks) and two small ones.
	big := make([]byte, 3000)
	for i := range big {
		big[i] = byte(i)
	}
	if _, err := im.AddFile("BIGSAMPLE", TypeSample, big); err != nil {
		t.Fatalf("AddFile big: %v", err)
	}
	if _, err := im.AddFile("PROG", TypeProgram, []byte{1, 2, 3, 4}); err != nil {
		t.Fatalf("AddFile prog: %v", err)
	}
	if _, err := im.AddFile(OverallFileName, TypeOverall, make([]byte, 40)); err != nil {
		t.Fatalf("AddFile ovs: %v", err)
	}

	// Parse from the raw bytes (fresh object) and verify contents.
	im2, err := Parse(im.Bytes())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	entries := im2.Entries()
	if len(entries) != 3 {
		t.Fatalf("Entries = %d, want 3 (%+v)", len(entries), entries)
	}
	if entries[0].Name != "BIGSAMPLE" || entries[0].Type != TypeSample || entries[0].Size != 3000 {
		t.Errorf("entry 0 = %+v", entries[0])
	}
	got, err := im2.ReadFile(entries[0])
	if err != nil {
		t.Fatalf("ReadFile big: %v", err)
	}
	if !bytes.Equal(got, big) {
		t.Error("big file did not round-trip")
	}
	if entries[2].Name != "OVERALL SE" {
		t.Errorf("entry 2 name = %q", entries[2].Name)
	}
}

func TestImage_SizeValidation(t *testing.T) {
	if _, err := Parse(make([]byte, 1000)); !errors.Is(err, ErrBadSize) {
		t.Errorf("expected ErrBadSize, got %v", err)
	}
}

func TestImage_HeaderBlocksNeverAllocated(t *testing.T) {
	im := New()
	e, err := im.AddFile("FIRST", TypeSample, []byte{1})
	if err != nil {
		t.Fatal(err)
	}
	if e.StartBlock < HeaderBlocks {
		t.Errorf("first file landed in header block %d", e.StartBlock)
	}
}

func TestImage_DiskFull(t *testing.T) {
	im := New()
	// One file consuming all 796 data blocks, then one more byte.
	huge := make([]byte, (TotalBlocksLD-HeaderBlocks)*BlockSize)
	if _, err := im.AddFile("HUGE", TypeSample, huge); err != nil {
		t.Fatalf("exact-fit file should succeed: %v", err)
	}
	if _, err := im.AddFile("MORE", TypeSample, []byte{1}); !errors.Is(err, ErrDiskFull) {
		t.Errorf("expected ErrDiskFull, got %v", err)
	}
}

func TestImage_DirFull(t *testing.T) {
	im := New()
	for i := 0; i < DirEntries; i++ {
		if _, err := im.AddFile("F", TypeSample, []byte{1}); err != nil {
			t.Fatalf("AddFile %d: %v", i, err)
		}
	}
	if _, err := im.AddFile("OVERFLOW", TypeSample, []byte{1}); !errors.Is(err, ErrDirFull) {
		t.Errorf("expected ErrDirFull, got %v", err)
	}
}

func TestImage_FATChainLoopDetected(t *testing.T) {
	im := New()
	e, err := im.AddFile("LOOPY", TypeSample, make([]byte, 2048))
	if err != nil {
		t.Fatal(err)
	}
	// Corrupt the FAT: second block points back at the first.
	im.setFAT(e.StartBlock+1, uint16(e.StartBlock))
	// Lie about the size so the chain has to be followed past the loop.
	entries := im.Entries()
	entries[0].Size = ImageSizeLD * 2
	if _, err := im.ReadFile(entries[0]); !errors.Is(err, ErrBadChain) {
		t.Errorf("expected ErrBadChain on FAT loop, got %v", err)
	}
}

// ---------- protocol-type builders ----------

func TestBuildSampleFile_HeaderMatchesSPRMFields(t *testing.T) {
	// Build a sample file from a synthetic SPRM and verify the
	// RAM-header fields land at the akai_sample900_s offsets:
	// name@0 (10 ASCII), slen@16 (4 LE), srate@20 (2 LE),
	// npitch@22, loud@24, pmode@26, end@28, start@32, llen@36,
	// type@42, dir@43 — i.e. the de-DB'd SPRM aligns byte-for-byte
	// with the RAM/disk struct (wire offset = 2 × RAM offset).
	words := make([]uint16, 100)
	for i := range words {
		words[i] = 0x800
	}
	p := &protocol.SampleParams{
		Name:         "TESTSAMP  ",
		TotalWords:   100,
		SampleRateHz: 26040,
		NominalPitch: 960,
		ReplayMode:   'L',
		Start:        0,
		End:          99,
		LoopLength:   99,
		Reversed:     'N',
	}
	f, err := BuildSampleFile(p, words)
	if err != nil {
		t.Fatalf("BuildSampleFile: %v", err)
	}
	wantLen := 60 + 3*50
	if len(f) != wantLen {
		t.Fatalf("file size = %d, want %d", len(f), wantLen)
	}
	if got := string(f[0:8]); got != "TESTSAMP" {
		t.Errorf("name = %q", got)
	}
	if slen := int(f[16]) | int(f[17])<<8 | int(f[18])<<16 | int(f[19])<<24; slen != 100 {
		t.Errorf("slen = %d, want 100", slen)
	}
	if rate := int(f[20]) | int(f[21])<<8; rate != 26040 {
		t.Errorf("srate = %d, want 26040", rate)
	}
	if pitch := int(f[22]) | int(f[23])<<8; pitch != 960 {
		t.Errorf("npitch = %d, want 960", pitch)
	}
	if f[26] != 'L' {
		t.Errorf("pmode = %q, want 'L'", f[26])
	}
	if f[43] != 'N' {
		t.Errorf("dir = %q, want 'N'", f[43])
	}
}

func TestBuildSampleFile_RejectsLengthMismatch(t *testing.T) {
	p := &protocol.SampleParams{TotalWords: 5}
	if _, err := BuildSampleFile(p, make([]uint16, 10)); err == nil {
		t.Fatal("expected error on TotalWords/words mismatch")
	}
}

func TestBuildProgramFile_SizeAndName(t *testing.T) {
	prog := protocol.NewDefaultProgram("DISKPROG", 2)
	f, err := BuildProgramFile(prog)
	if err != nil {
		t.Fatalf("BuildProgramFile: %v", err)
	}
	// 38-byte header + 2 × 70-byte keygroups.
	if len(f) != 38+2*70 {
		t.Fatalf("program file size = %d, want %d", len(f), 38+2*70)
	}
	if got := string(f[0:8]); got != "DISKPROG" {
		t.Errorf("program name on disk = %q", got)
	}
	// kgnum at RAM offset 17 (name[10]+dummy1[6]+dummy2[2]=18? —
	// header layout: name 0..9, dummy1 10..15, dummy2 16..17,
	// kg1a 18..19, dummy3 20, kgxf 21, dummy4 22, kgnum 23.
	if f[23] != 2 {
		t.Errorf("kgnum = %d, want 2", f[23])
	}
}

func TestBuildOverallFile_ForceRS232PatchesCtrlport(t *testing.T) {
	o := &protocol.OverallSettings{ProgName: "BOOTVOL   ", ControllerSelect: 1, BaudRate: 50000}
	plain, err := BuildOverallFile(o, false)
	if err != nil {
		t.Fatalf("BuildOverallFile: %v", err)
	}
	if len(plain) != 40 {
		t.Fatalf("OVS file size = %d, want 40", len(plain))
	}
	if plain[29] != 1 {
		t.Errorf("ctrlport (unforced) = %d, want 1 (MIDI)", plain[29])
	}
	forced, err := BuildOverallFile(o, true)
	if err != nil {
		t.Fatal(err)
	}
	if forced[29] != 2 {
		t.Errorf("ctrlport (forced) = %d, want 2 (RS-232C)", forced[29])
	}
	// Baud field at RAM 36..37 = baud/10 = 5000.
	if baud := int(forced[36]) | int(forced[37])<<8; baud != 5000 {
		t.Errorf("rs232brate10 = %d, want 5000", baud)
	}
}
