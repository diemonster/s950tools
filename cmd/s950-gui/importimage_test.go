package main

import (
	"strings"
	"testing"

	"github.com/bivers/s950/internal/akaidisk"
	"github.com/bivers/s950/internal/protocol"
)

// Tests for importGotekImage — the read half of the patch format.
// The disk-format layer is tested in internal/akaidisk; these pin
// the import policy: export→import round-trip fidelity, graceful
// per-file skipping, the nothing-importable guard.

func TestImportGotekImage_RoundTripsExport(t *testing.T) {
	// Build with the EXPORT core, read back with the IMPORT core —
	// the full patch-format contract at the binding level.
	req := ExportImageRequest{
		Programs: []protocol.ProgramJSON{protocol.NewDefaultProgram("RTKIT", 2).ToJSON()},
		Samples: []ExportImageSample{
			exportSample("RTKICK", 1000),
			exportSample("RTSNARE", 501), // odd count
		},
		ForceRS232: true,
	}
	im, err := buildGotekImage(req, defaultOVS())
	if err != nil {
		t.Fatal(err)
	}
	res, err := importGotekImage(im.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Samples) != 2 || len(res.Programs) != 1 {
		t.Fatalf("imported %d samples / %d programs, want 2 / 1 (skipped: %v)",
			len(res.Samples), len(res.Programs), res.Skipped)
	}
	if got := strings.TrimRight(res.Samples[0].Params.Name, " "); got != "RTKICK" {
		t.Errorf("sample 0 name = %q", got)
	}
	if len(res.Samples[1].Words) != 501 {
		t.Errorf("odd-count sample words = %d, want 501", len(res.Samples[1].Words))
	}
	if res.Programs[0].Name != "RTKIT" && !strings.HasPrefix(res.Programs[0].Name, "RTKIT") {
		t.Errorf("program name = %q", res.Programs[0].Name)
	}
	// OVERALL SE is intentionally not imported and not "skipped".
	for _, s := range res.Skipped {
		if strings.Contains(s, "OVERALL") {
			t.Errorf("OVERALL SE should be silently ignored, got skip entry %q", s)
		}
	}
}

func TestImportGotekImage_SkipsCompressedAndUnknownTypes(t *testing.T) {
	im := akaidisk.New()
	// A fixups-style file (type 'F') and a fake compressed sample.
	if _, err := im.AddFile("FIXUPS", 'F', make([]byte, 49)); err != nil {
		t.Fatal(err)
	}
	sf, err := akaidisk.BuildSampleFile(&protocol.SampleParams{
		Name: "GOODSAMP  ", TotalWords: 10, SampleRateHz: 26040,
		NominalPitch: 960, ReplayMode: 'O', Reversed: 'N', End: 9,
	}, make([]uint16, 10))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := im.AddFile("GOODSAMP", akaidisk.TypeSample, sf); err != nil {
		t.Fatal(err)
	}
	// Mark a second sample compressed by poking its dir entry's
	// osver field (entry 2 → offset 2*24+22).
	if _, err := im.AddFile("COMPSAMP", akaidisk.TypeSample, sf); err != nil {
		t.Fatal(err)
	}
	im.Bytes()[2*24+22] = 7

	res, err := importGotekImage(im.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Samples) != 1 {
		t.Errorf("imported %d samples, want 1 (the non-compressed one)", len(res.Samples))
	}
	joined := strings.Join(res.Skipped, "; ")
	if !strings.Contains(joined, "FIXUPS") {
		t.Errorf("FIXUPS not in skipped list: %v", res.Skipped)
	}
	if !strings.Contains(joined, "COMPSAMP") || !strings.Contains(joined, "compressed") {
		t.Errorf("compressed sample not skipped with reason: %v", res.Skipped)
	}
}

func TestImportGotekImage_NothingImportable(t *testing.T) {
	im := akaidisk.New()
	if _, err := im.AddFile("FIXUPS", 'F', make([]byte, 49)); err != nil {
		t.Fatal(err)
	}
	if _, err := importGotekImage(im.Bytes()); err == nil {
		t.Fatal("expected error when image has nothing importable")
	}
}

func TestImportGotekImage_RejectsNonImage(t *testing.T) {
	if _, err := importGotekImage(make([]byte, 5000)); err == nil {
		t.Fatal("expected size error for non-image input")
	}
}
