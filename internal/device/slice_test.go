package device

import (
	"errors"
	"strings"
	"testing"

	"github.com/bivers/s950/internal/sample"
)

// makeSource returns a length-N word buffer where word[i] = uint16(i),
// so we can verify that BuildSlices copies the right region (each
// slice's payload's first word should equal its StartWord).
func makeSource(n int) []uint16 {
	out := make([]uint16, n)
	for i := range out {
		out[i] = uint16(i & 0xFFF) // 12-bit
	}
	return out
}

func TestBuildSlices_ExtractsRegions(t *testing.T) {
	src := makeSource(4000)
	specs := []SliceSpec{
		{Name: "A", StartWord: 0, LengthWords: 1000, LoopMode: "one-shot"},
		{Name: "B", StartWord: 1000, LengthWords: 1000, LoopMode: "loop", LoopStartInSlice: 100, LoopLengthInSlice: 500},
		{Name: "C", StartWord: 2000, LengthWords: 1000, LoopMode: "ping-pong", LoopStartInSlice: 0, LoopLengthInSlice: 900},
	}
	got, err := BuildSlices(src, 26040, specs)
	if err != nil {
		t.Fatalf("BuildSlices: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len=%d, want 3", len(got))
	}
	// First word of each slice should equal its source start word.
	for i, sl := range got {
		want := uint16(specs[i].StartWord & 0xFFF)
		if sl.Words[0] != want {
			t.Errorf("slice %d: first word = %d, want %d", i, sl.Words[0], want)
		}
		if uint32(len(sl.Words)) != specs[i].LengthWords {
			t.Errorf("slice %d: len=%d, want %d", i, len(sl.Words), specs[i].LengthWords)
		}
	}
}

func TestBuildSlices_OneShotForcesSentinel(t *testing.T) {
	src := makeSource(2000)
	got, err := BuildSlices(src, 26040, []SliceSpec{
		{Name: "X", StartWord: 0, LengthWords: 500, LoopMode: "one-shot"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// loop_start should be >= total-5 (== 495) for one-shot playback.
	if got[0].Opts.LoopStart < 495 {
		t.Errorf("one-shot LoopStart = %d, want >= 495 (length-5)", got[0].Opts.LoopStart)
	}
	if got[0].Opts.Mode != 0 {
		t.Errorf("one-shot Mode = %d, want 0", got[0].Opts.Mode)
	}
}

func TestBuildSlices_PingPongMode(t *testing.T) {
	src := makeSource(2000)
	got, err := BuildSlices(src, 26040, []SliceSpec{
		{Name: "X", StartWord: 0, LengthWords: 500,
			LoopMode: "ping-pong", LoopStartInSlice: 50, LoopLengthInSlice: 400},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Opts.Mode != 1 {
		t.Errorf("ping-pong Mode = %d, want 1 (alt)", got[0].Opts.Mode)
	}
	if got[0].Opts.LoopStart != 50 || got[0].Opts.LoopEnd != 450 {
		t.Errorf("loop = [%d..%d], want [50..450]", got[0].Opts.LoopStart, got[0].Opts.LoopEnd)
	}
}

func TestBuildSlices_TinyLoopFallsBackToOneShot(t *testing.T) {
	src := makeSource(2000)
	got, err := BuildSlices(src, 26040, []SliceSpec{
		{Name: "X", StartWord: 0, LengthWords: 500,
			LoopMode: "loop", LoopStartInSlice: 100, LoopLengthInSlice: 3}, // < 5
	})
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Opts.LoopStart < 495 || got[0].Opts.Mode != 0 {
		t.Errorf("tiny loop should fall back to one-shot, got LoopStart=%d Mode=%d",
			got[0].Opts.LoopStart, got[0].Opts.Mode)
	}
}

func TestBuildSlices_PropagatesRate(t *testing.T) {
	src := makeSource(2000)
	got, _ := BuildSlices(src, 22050, []SliceSpec{
		{Name: "X", StartWord: 0, LengthWords: 500},
	})
	if got[0].Opts.SampleRateHz != 22050 {
		t.Errorf("rate not propagated: got %d, want 22050", got[0].Opts.SampleRateHz)
	}
}

func TestBuildSlices_RejectsOutOfRange(t *testing.T) {
	src := makeSource(1000)
	_, err := BuildSlices(src, 26040, []SliceSpec{
		{Name: "X", StartWord: 800, LengthWords: 500},
	})
	if !errors.Is(err, ErrSliceOutOfRange) {
		t.Errorf("got %v, want ErrSliceOutOfRange", err)
	}
}

func TestBuildSlices_RejectsBelowMin(t *testing.T) {
	src := makeSource(1000)
	_, err := BuildSlices(src, 26040, []SliceSpec{
		{Name: "X", StartWord: 0, LengthWords: sample.MinTotalWords - 1},
	})
	if !errors.Is(err, ErrSliceTooShort) {
		t.Errorf("got %v, want ErrSliceTooShort", err)
	}
}

func TestBuildSlices_RejectsEmpty(t *testing.T) {
	_, err := BuildSlices(makeSource(1000), 26040, nil)
	if !errors.Is(err, ErrNoSlices) {
		t.Errorf("got %v, want ErrNoSlices", err)
	}
}

func TestBuildSlices_CopyIsIndependent(t *testing.T) {
	src := makeSource(1000)
	got, _ := BuildSlices(src, 26040, []SliceSpec{
		{Name: "X", StartWord: 0, LengthWords: 500},
	})
	// Mutating the source after extraction should not affect the slice.
	for i := range src {
		src[i] = 0
	}
	if got[0].Words[100] == 0 {
		t.Error("slice payload was not copied — host mutation leaked into the slice")
	}
}

func TestSanitizeName_StripsNonAsciiAndTruncates(t *testing.T) {
	got := SanitizeName("AMÉNBREAKS_LONG")
	if len(got) > 10 {
		t.Errorf("not truncated: %q (len=%d)", got, len(got))
	}
	for _, r := range got {
		if r < 0x20 || r >= 0x7F {
			t.Errorf("non-ascii leaked: %q", got)
		}
	}
}

func TestSliceNames_FitsTenChars(t *testing.T) {
	for _, n := range []int{1, 8, 16, 31} {
		names := SliceNames("AMENBREAKS", n)
		if len(names) != n {
			t.Fatalf("n=%d: got %d names", n, len(names))
		}
		for i, name := range names {
			if len(name) > 10 {
				t.Errorf("n=%d slice %d: %q exceeds 10 chars", n, i, name)
			}
			if !strings.HasSuffix(name, "_01") && i == 0 {
				t.Errorf("first slice not suffixed _01: %q", name)
			}
		}
	}
}

func TestBuildSliceProgram_OneKeygroupPerSlice(t *testing.T) {
	specs := []SliceSpec{
		{Name: "BEAT_01"}, {Name: "BEAT_02"}, {Name: "BEAT_03"},
	}
	p, err := BuildSliceProgram("BEAT", specs, 36) // C2
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Keygroups) != 3 {
		t.Fatalf("got %d keygroups, want 3", len(p.Keygroups))
	}
	for i, kg := range p.Keygroups {
		wantKey := uint8(36 + i)
		if kg.LowerKey != wantKey || kg.UpperKey != wantKey {
			t.Errorf("kg %d: keys [%d..%d], want [%d..%d]",
				i, kg.LowerKey, kg.UpperKey, wantKey, wantKey)
		}
		if kg.VelocitySwitch != 128 {
			t.Errorf("kg %d: VelocitySwitch=%d, want 128 (soft only)", i, kg.VelocitySwitch)
		}
		if kg.SoftSampleName != specs[i].Name {
			t.Errorf("kg %d: SoftSampleName=%q, want %q", i, kg.SoftSampleName, specs[i].Name)
		}
	}
	if p.Name != "BEAT" {
		t.Errorf("program name = %q, want %q", p.Name, "BEAT")
	}
}

func TestBuildSliceProgram_RejectsTooManySlices(t *testing.T) {
	specs := make([]SliceSpec, 32) // > MaxKeygroups (31)
	_, err := BuildSliceProgram("X", specs, 36)
	if !errors.Is(err, ErrTooManySlices) {
		t.Errorf("got %v, want ErrTooManySlices", err)
	}
}

func TestBuildSliceProgram_RejectsMappingPast127(t *testing.T) {
	specs := make([]SliceSpec, 8)
	_, err := BuildSliceProgram("X", specs, 125) // 125..132, hits 128+
	if err == nil {
		t.Errorf("expected error for mapping past MIDI 127")
	}
}
