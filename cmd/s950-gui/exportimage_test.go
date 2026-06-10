package main

import (
	"os"
	"strings"
	"testing"

	"github.com/bivers/s950/internal/akaidisk"
	"github.com/bivers/s950/internal/protocol"
)

// Tests for buildGotekImage — the pure assembly core behind the
// "Export Gotek image…" binding. Disk-format correctness itself is
// covered (and oracle-verified) in internal/akaidisk; these tests
// pin the export policy: name collisions, raw seeding for local
// programs, OVS forcing, capacity surfacing, empty-input guard.

func exportSample(name string, n int) ExportImageSample {
	words := make([]uint16, n)
	for i := range words {
		words[i] = 0x800
	}
	return ExportImageSample{
		Params: protocol.SampleParams{
			Name: name, TotalWords: uint32(n), SampleRateHz: 26040,
			NominalPitch: 960, ReplayMode: 'O', End: uint32(n - 1), Reversed: 'N',
		},
		Words: words,
	}
}

func defaultOVS() *protocol.OverallSettings {
	return &protocol.OverallSettings{ProgName: "TESTVOL   ", BaudRate: 50000}
}

func TestBuildGotekImage_HappyPath(t *testing.T) {
	prog := protocol.NewDefaultProgram("MYKIT", 2).ToJSON()
	req := ExportImageRequest{
		Programs:   []protocol.ProgramJSON{prog},
		Samples:    []ExportImageSample{exportSample("KICK", 1000), exportSample("SNARE", 500)},
		ForceRS232: true,
	}
	im, err := buildGotekImage(req, defaultOVS())
	if err != nil {
		t.Fatalf("buildGotekImage: %v", err)
	}
	entries := im.Entries()
	// program + OVERALL SE + 2 samples.
	if len(entries) != 4 {
		t.Fatalf("entries = %d, want 4 (%+v)", len(entries), entries)
	}
	// Order mirrors a sampler-written disk: programs, OVS, samples.
	if entries[0].Type != akaidisk.TypeProgram || entries[1].Name != akaidisk.OverallFileName {
		t.Errorf("unexpected layout: %+v", entries)
	}
	// The OVS file must carry the forced RS-232 controller-select.
	ovsBytes, err := im.ReadFile(entries[1])
	if err != nil {
		t.Fatal(err)
	}
	if ovsBytes[29] != 2 {
		t.Errorf("ctrlport = %d, want 2 (RS-232C)", ovsBytes[29])
	}
}

func TestBuildGotekImage_NoForceKeepsDeviceCtrlport(t *testing.T) {
	req := ExportImageRequest{
		Samples:    []ExportImageSample{exportSample("KICK", 100)},
		ForceRS232: false,
	}
	ovs := defaultOVS()
	ovs.ControllerSelect = 1
	im, err := buildGotekImage(req, ovs)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range im.Entries() {
		if e.Type == akaidisk.TypeOverall {
			b, _ := im.ReadFile(e)
			if b[29] != 1 {
				t.Errorf("ctrlport = %d, want 1 (unforced)", b[29])
			}
		}
	}
}

func TestBuildGotekImage_RejectsEmptyRequest(t *testing.T) {
	if _, err := buildGotekImage(ExportImageRequest{}, defaultOVS()); err == nil {
		t.Fatal("expected error for empty export")
	}
}

func TestBuildGotekImage_RejectsDuplicateDiskNames(t *testing.T) {
	// Two samples whose names sanitize identically must be a hard
	// error — the S950 loads by name and one would shadow the other.
	req := ExportImageRequest{
		Samples: []ExportImageSample{
			exportSample("LONGSAMPLENAME1", 100), // → LONGSAMPLE
			exportSample("LONGSAMPLENAME2", 100), // → LONGSAMPLE
		},
	}
	_, err := buildGotekImage(req, defaultOVS())
	if err == nil || !strings.Contains(err.Error(), "duplicate disk name") {
		t.Fatalf("expected duplicate-name error, got %v", err)
	}
}

func TestBuildGotekImage_RejectsSampleWithoutAudio(t *testing.T) {
	s := exportSample("EMPTY", 100)
	s.Words = nil
	_, err := buildGotekImage(ExportImageRequest{Samples: []ExportImageSample{s}}, defaultOVS())
	if err == nil || !strings.Contains(err.Error(), "no host audio") {
		t.Fatalf("expected no-host-audio error, got %v", err)
	}
}

func TestBuildGotekImage_SeedsLocalProgramRaws(t *testing.T) {
	// A locally-built program (New Program button) has empty raw hex
	// fields; export must seed them instead of failing FromJSON.
	prog := protocol.NewDefaultProgram("LOCAL", 2).ToJSON()
	prog.RawHeaderHex = ""
	for i := range prog.Keygroups {
		prog.Keygroups[i].RawBytesHex = ""
	}
	req := ExportImageRequest{Programs: []protocol.ProgramJSON{prog}}
	im, err := buildGotekImage(req, defaultOVS())
	if err != nil {
		t.Fatalf("local program should export with seeded raws: %v", err)
	}
	for _, e := range im.Entries() {
		if e.Type == akaidisk.TypeProgram {
			f, _ := im.ReadFile(e)
			if len(f) != 38+2*70 {
				t.Errorf("program file size = %d, want %d", len(f), 38+2*70)
			}
			if f[23] != 2 {
				t.Errorf("kgnum = %d, want 2", f[23])
			}
		}
	}
}

func TestBuildGotekImage_CapacityOverflowSurfaces(t *testing.T) {
	// ~796 KB of data blocks available; ask for ~900 KB of samples.
	req := ExportImageRequest{
		Samples: []ExportImageSample{
			exportSample("BIGONE", 300000), // 60 + 450000 B → 440 blocks
			exportSample("BIGTWO", 300000), // another 440 → overflow
		},
	}
	_, err := buildGotekImage(req, defaultOVS())
	if err == nil || !strings.Contains(err.Error(), "out of free blocks") {
		t.Fatalf("expected disk-full error, got %v", err)
	}
}

// TestBuildGotekImage_WriteForOracle writes a GUI-shaped export to
// $EXPORT_IMG for manual verification with the akaiutil oracle:
//
//	EXPORT_IMG=/tmp/export.img go test ./cmd/s950-gui -run WriteForOracle
//	printf 'infoall\nexit\n' | akaiutil -r -f /tmp/export.img
//
// Skips by default — the automated oracle coverage of the disk
// format lives in internal/akaidisk/oracle_test.go.
func TestBuildGotekImage_WriteForOracle(t *testing.T) {
	out := os.Getenv("EXPORT_IMG")
	if out == "" {
		t.Skip("set EXPORT_IMG to write a GUI-shaped image for oracle inspection")
	}
	prog := protocol.NewDefaultProgram("ORACLEKIT", 2).ToJSON()
	req := ExportImageRequest{
		Programs:   []protocol.ProgramJSON{prog},
		Samples:    []ExportImageSample{exportSample("ORAKICK", 2000), exportSample("ORASNARE", 1500)},
		ForceRS232: true,
	}
	im, err := buildGotekImage(req, defaultOVS())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, im.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestBuildGotekImage_ForcesTotalWordsConsistency(t *testing.T) {
	// SPRM claims 999 words but 1000 are supplied — the export
	// must trust the audio and fix the header, not error or write
	// a lying header the S950's loader would mis-read.
	s := exportSample("FIXLEN", 1000)
	s.Params.TotalWords = 999
	im, err := buildGotekImage(ExportImageRequest{Samples: []ExportImageSample{s}}, defaultOVS())
	if err != nil {
		t.Fatalf("buildGotekImage: %v", err)
	}
	for _, e := range im.Entries() {
		if e.Type == akaidisk.TypeSample {
			f, _ := im.ReadFile(e)
			slen := int(f[16]) | int(f[17])<<8 | int(f[18])<<16 | int(f[19])<<24
			if slen != 1000 {
				t.Errorf("disk header slen = %d, want 1000", slen)
			}
		}
	}
}
