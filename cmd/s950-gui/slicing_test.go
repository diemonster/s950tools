package main

import (
	"strings"
	"testing"

	"github.com/bivers/s950/internal/device"
	"github.com/bivers/s950/internal/protocol"
)

// Tests for planSlicing — the pure planner extracted from
// inspectSlicingLocked. Validates the slot-allocation matrix
// (auto vs manual, occupied warnings, TONE carve-out, overflows)
// + the time-estimate math, without spinning up a device.

// buildReq produces a valid 3-slice request against a 600-word
// source. Auto slot picking; caller can override fields per test.
func buildReq() SlicingRequest {
	words := make([]uint16, 600)
	return SlicingRequest{
		SourceWords:     words,
		SourceRateHz:    26040,
		Slices:          []device.SliceSpec{
			{Name: "S1", StartWord: 0, LengthWords: 200},
			{Name: "S2", StartWord: 200, LengthWords: 200},
			{Name: "S3", StartWord: 400, LengthWords: 200},
		},
		BaseName:        "KIT",
		ProgramName:     "KITPROG",
		BaseMidiKey:     60,
		FirstSampleSlot: -1, // auto
		ProgramSlot:     -1, // auto
	}
}

func TestPlanSlicing_HappyPath_AutoPicksLowestFreeBlock(t *testing.T) {
	// Empty catalog → samples land at 0..2, program at 0, no errors.
	got := planSlicing(nil, buildReq())
	if !got.OK {
		t.Fatalf("expected OK=true, errors=%v", got.Errors)
	}
	if want := []int{0, 1, 2}; !equalInts(got.SampleSlots, want) {
		t.Errorf("SampleSlots = %v, want %v", got.SampleSlots, want)
	}
	if got.ProgramSlot != 0 {
		t.Errorf("ProgramSlot = %d, want 0", got.ProgramSlot)
	}
	if len(got.Warnings) != 0 {
		t.Errorf("expected no warnings, got %v", got.Warnings)
	}
	if got.EstimatedSeconds <= 0 {
		t.Errorf("EstimatedSeconds should be > 0, got %d", got.EstimatedSeconds)
	}
}

func TestPlanSlicing_AutoSkipsOccupiedSlots(t *testing.T) {
	// 0..1 occupied by real samples, 2..4 free → auto-picks block at 2.
	entries := []protocol.CatalogEntry{
		{Type: 'S', Num: 0, Name: "KICK"},
		{Type: 'S', Num: 1, Name: "SNARE"},
	}
	got := planSlicing(entries, buildReq())
	if !got.OK {
		t.Fatalf("expected OK=true, errors=%v", got.Errors)
	}
	if want := []int{2, 3, 4}; !equalInts(got.SampleSlots, want) {
		t.Errorf("SampleSlots = %v, want %v", got.SampleSlots, want)
	}
	if len(got.OccupiedSlots) != 0 {
		t.Errorf("auto-pick should avoid occupied slots; got %v", got.OccupiedSlots)
	}
}

func TestPlanSlicing_TONEPlaceholdersTreatedAsFree(t *testing.T) {
	// All slots labelled "TONE" — auto-pick must still land at 0.
	// Pin: TONE is the S950's boot placeholder, safe to overwrite,
	// and must not generate an occupied warning.
	entries := []protocol.CatalogEntry{
		{Type: 'S', Num: 0, Name: "TONE"},
		{Type: 'S', Num: 1, Name: "TONE"},
		{Type: 'S', Num: 2, Name: "TONE"},
		{Type: 'P', Num: 0, Name: "TONE"},
	}
	got := planSlicing(entries, buildReq())
	if !got.OK {
		t.Fatalf("expected OK=true, errors=%v", got.Errors)
	}
	if got.SampleSlots[0] != 0 {
		t.Errorf("TONE should not block auto-pick; SampleSlots[0] = %d, want 0", got.SampleSlots[0])
	}
	if len(got.OccupiedSlots) != 0 {
		t.Errorf("TONE entries should not surface as occupied; got %v", got.OccupiedSlots)
	}
}

func TestPlanSlicing_ManualSlotSurfacesOccupiedAsWarning(t *testing.T) {
	// Manual firstSlot=5 hits an occupied slot → preflight still OK
	// (warnings don't block) but reports the overwrite.
	req := buildReq()
	req.FirstSampleSlot = 5
	req.ProgramSlot = 10
	entries := []protocol.CatalogEntry{
		{Type: 'S', Num: 6, Name: "KICK808"},
		{Type: 'P', Num: 10, Name: "OLDPRG"},
	}
	got := planSlicing(entries, req)
	if !got.OK {
		t.Fatalf("warnings should not block OK, errors=%v", got.Errors)
	}
	if want := []int{5, 6, 7}; !equalInts(got.SampleSlots, want) {
		t.Errorf("manual slot block = %v, want %v", got.SampleSlots, want)
	}
	if len(got.OccupiedSlots) != 2 {
		t.Fatalf("expected 2 occupied slot reports, got %d (%v)", len(got.OccupiedSlots), got.OccupiedSlots)
	}
	// One per occupied slot, each surfaced as a warning string.
	if len(got.Warnings) != 2 {
		t.Errorf("expected 2 warnings, got %d (%v)", len(got.Warnings), got.Warnings)
	}
	for _, w := range got.Warnings {
		if !strings.Contains(w, "overwritten") {
			t.Errorf("warning should mention overwrite: %q", w)
		}
	}
}

func TestPlanSlicing_SlotOverflowReportsError(t *testing.T) {
	// firstSlot=98 + 3 slices → would write to 98, 99, 100. 100 > 99 max.
	req := buildReq()
	req.FirstSampleSlot = 98
	got := planSlicing(nil, req)
	if got.OK {
		t.Fatal("expected OK=false on slot overflow")
	}
	if !containsErr(got.Errors, "exceeds S950 maximum") {
		t.Errorf("expected 'exceeds S950 maximum' error, got %v", got.Errors)
	}
}

func TestPlanSlicing_ProgramSlotOutOfRange(t *testing.T) {
	req := buildReq()
	req.ProgramSlot = 100
	got := planSlicing(nil, req)
	if got.OK {
		t.Fatal("expected OK=false on programSlot=100")
	}
	if !containsErr(got.Errors, "outside 0..99") {
		t.Errorf("expected 'outside 0..99' error, got %v", got.Errors)
	}
}

func TestPlanSlicing_NoFreeProgramSlot(t *testing.T) {
	// Every program slot taken by a non-TONE name → no auto-pick possible.
	var entries []protocol.CatalogEntry
	for i := byte(0); i < 100; i++ {
		entries = append(entries, protocol.CatalogEntry{Type: 'P', Num: i, Name: "P"})
	}
	got := planSlicing(entries, buildReq())
	if got.OK {
		t.Fatal("expected OK=false when no program slot is free")
	}
	if !containsErr(got.Errors, "no free program slots") {
		t.Errorf("expected 'no free program slots' error, got %v", got.Errors)
	}
}

func TestPlanSlicing_NoConsecutiveFreeBlock(t *testing.T) {
	// Occupy odd-numbered slots so no run of 3 consecutive free slots exists.
	var entries []protocol.CatalogEntry
	for i := byte(0); i < 100; i++ {
		if i%2 == 1 {
			entries = append(entries, protocol.CatalogEntry{Type: 'S', Num: i, Name: "X"})
		}
	}
	got := planSlicing(entries, buildReq())
	if got.OK {
		t.Fatal("expected OK=false when no 3-slot run exists")
	}
	if !containsErr(got.Errors, "no run of 3") {
		t.Errorf("expected 'no run of 3' error, got %v", got.Errors)
	}
}

func TestPlanSlicing_PropagatesBuildSlicesError(t *testing.T) {
	// Slice asks for words past the end of the source — BuildSlices
	// returns ErrSliceOutOfRange and the planner reports it.
	req := buildReq()
	req.Slices[0].LengthWords = 99999 // way past source
	got := planSlicing(nil, req)
	if got.OK {
		t.Fatal("expected OK=false on out-of-range slice")
	}
	if !containsErr(got.Errors, "slice") {
		t.Errorf("expected slice-related error from BuildSlices, got %v", got.Errors)
	}
}

func TestPlanSlicing_RejectsTooManySlices(t *testing.T) {
	// 32 slices > MaxKeygroups (31). Use a bigger source so BuildSlices
	// doesn't fail first on out-of-range — we want the planner's own
	// keygroup-count check to fire.
	req := buildReq()
	req.SourceWords = make([]uint16, 32*200)
	req.Slices = nil
	for i := 0; i < 32; i++ {
		req.Slices = append(req.Slices, device.SliceSpec{
			Name:        "S",
			StartWord:   uint32(i * 200),
			LengthWords: 200,
		})
	}
	got := planSlicing(nil, req)
	if got.OK {
		t.Fatal("expected OK=false on > MaxKeygroups slices")
	}
	if !containsErr(got.Errors, "too many slices") {
		t.Errorf("expected 'too many slices' error, got %v", got.Errors)
	}
}

func TestPlanSlicing_RejectsBaseKeyOverflow(t *testing.T) {
	// BaseMidiKey=126 + 3 slices → highest key 128, past MIDI 127.
	req := buildReq()
	req.BaseMidiKey = 126
	got := planSlicing(nil, req)
	if got.OK {
		t.Fatal("expected OK=false when base+N-1 > 127")
	}
	if !containsErr(got.Errors, "past MIDI 127") {
		t.Errorf("expected 'past MIDI 127' error, got %v", got.Errors)
	}
}

func TestPlanSlicing_EstimateScalesWithSampleSize(t *testing.T) {
	// 600-word source vs 6000-word source — the larger one must
	// estimate a longer (or equal — small samples hit ExpectedDrainTime's
	// 200 ms floor) transfer time.
	smallReq := buildReq()

	big := buildReq()
	big.SourceWords = make([]uint16, 6000)
	big.Slices = []device.SliceSpec{
		{Name: "X", StartWord: 0, LengthWords: 2000},
		{Name: "Y", StartWord: 2000, LengthWords: 2000},
		{Name: "Z", StartWord: 4000, LengthWords: 2000},
	}

	smallEst := planSlicing(nil, smallReq).EstimatedSeconds
	bigEst := planSlicing(nil, big).EstimatedSeconds
	if bigEst < smallEst {
		t.Errorf("6000-word estimate (%d s) should be >= 600-word estimate (%d s)", bigEst, smallEst)
	}
}

func TestPlanSlicing_ProgramSlotOccupiedSurfacesAsWarning(t *testing.T) {
	// Auto sample pick, manual program slot pointing at a real entry.
	req := buildReq()
	req.ProgramSlot = 5
	entries := []protocol.CatalogEntry{
		{Type: 'P', Num: 5, Name: "OLDPRG"},
	}
	got := planSlicing(entries, req)
	if !got.OK {
		t.Fatalf("warnings shouldn't block OK, errors=%v", got.Errors)
	}
	if got.ProgramSlot != 5 {
		t.Errorf("ProgramSlot = %d, want 5", got.ProgramSlot)
	}
	if len(got.OccupiedSlots) != 1 {
		t.Fatalf("expected one occupied report, got %v", got.OccupiedSlots)
	}
	if got.OccupiedSlots[0].Kind != "program" || got.OccupiedSlots[0].Name != "OLDPRG" {
		t.Errorf("unexpected occupied entry: %+v", got.OccupiedSlots[0])
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func containsErr(errs []string, substr string) bool {
	for _, e := range errs {
		if strings.Contains(e, substr) {
			return true
		}
	}
	return false
}
