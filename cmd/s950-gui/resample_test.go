package main

import (
	"testing"

	"github.com/bivers/s950/internal/sample"
)

// ResampleSample is the host-side resample binding the Sample-tab
// rate dropdown calls. Pure host op (no wire traffic, no app state
// access), so a zero-value App suffices for testing.

func TestResampleSample_HappyPath_UpsamplesPCM(t *testing.T) {
	a := &App{}
	// 4 samples at 22050 Hz → expect ~8 samples at 44100 Hz (2×).
	pcm := []int16{1000, 2000, 3000, 4000}
	out, err := a.ResampleSample(pcm, 22050, 44100)
	if err != nil {
		t.Fatalf("ResampleSample: %v", err)
	}
	if out.Rate != 44100 {
		t.Errorf("Rate = %d, want 44100", out.Rate)
	}
	// The resampler doesn't always produce exactly 2× length (it
	// uses a polyphase filter with edge handling), so allow ±2.
	expected := len(pcm) * 2
	if got := len(out.PCM); got < expected-2 || got > expected+2 {
		t.Errorf("upsampled len = %d, want ~%d", got, expected)
	}
	if uint32(len(out.PCM)) != out.Length {
		t.Errorf("Length field %d != len(PCM) %d", out.Length, len(out.PCM))
	}
	// Words array is the same audio in 12-bit S950 form. Must
	// match PCM length 1:1 since PCM16toSW is per-sample.
	if len(out.Words) != len(out.PCM) {
		t.Errorf("len(Words) %d != len(PCM) %d", len(out.Words), len(out.PCM))
	}
}

func TestResampleSample_HappyPath_DownsamplesPCM(t *testing.T) {
	a := &App{}
	// 8 samples at 44100 Hz → expect ~4 samples at 22050 Hz (½).
	pcm := make([]int16, 1000)
	for i := range pcm {
		pcm[i] = int16(i % 100)
	}
	out, err := a.ResampleSample(pcm, 44100, 22050)
	if err != nil {
		t.Fatalf("ResampleSample: %v", err)
	}
	if out.Rate != 22050 {
		t.Errorf("Rate = %d, want 22050", out.Rate)
	}
	if len(out.PCM) >= len(pcm) {
		t.Errorf("downsampled len %d not less than source %d", len(out.PCM), len(pcm))
	}
}

func TestResampleSample_ClampsBelowS950Range(t *testing.T) {
	a := &App{}
	pcm := make([]int16, 500)
	// Ask for 1 kHz — well below the S950's min (~2 kHz). The
	// underlying ResampleTo clamps to the supported range; verify
	// the returned Rate reflects what actually got applied, not
	// what we asked for.
	out, err := a.ResampleSample(pcm, 22050, 1)
	if err != nil {
		t.Fatalf("ResampleSample: %v", err)
	}
	if out.Rate < sample.MinSampleRateHz {
		t.Errorf("Rate %d below S950 min %d (clamp didn't fire)", out.Rate, sample.MinSampleRateHz)
	}
}

func TestResampleSample_RejectsEmptyPCM(t *testing.T) {
	a := &App{}
	_, err := a.ResampleSample(nil, 22050, 44100)
	if err == nil {
		t.Fatal("expected error on nil PCM")
	}
	_, err = a.ResampleSample([]int16{}, 22050, 44100)
	if err == nil {
		t.Fatal("expected error on empty PCM")
	}
}

func TestResampleSample_RejectsZeroRates(t *testing.T) {
	a := &App{}
	pcm := []int16{0, 0, 0, 0}
	if _, err := a.ResampleSample(pcm, 0, 44100); err == nil {
		t.Error("expected error on fromRate=0")
	}
	if _, err := a.ResampleSample(pcm, 22050, 0); err == nil {
		t.Error("expected error on toRate=0")
	}
}
