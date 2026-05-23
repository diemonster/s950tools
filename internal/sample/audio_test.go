package sample

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-audio/aiff"
	"github.com/go-audio/audio"
	"github.com/go-audio/wav"
)

func TestParseRate(t *testing.T) {
	tests := []struct {
		in      string
		want    uint32
		wantErr bool
	}{
		{"", 0, false},
		{"0", 0, false},
		{"44100", 44100, false},
		{"22050", 22050, false},
		{"sp1200", 26040, false},
		{"SP1200", 26040, false}, // case-insensitive
		{"mpc60", 40000, false},
		{"lofi", 10000, false},
		{"telephone", 8000, false},
		{"s950-10", 10000, false},
		{"s950-40", 40000, false},
		{"  sp1200  ", 26040, false}, // whitespace
		{"unknown-alias", 0, true},
		{"-100", 0, true},  // negative
		{"abc", 0, true},
		{"1.5", 0, true},   // not integer
	}
	for _, tt := range tests {
		got, err := ParseRate(tt.in)
		if tt.wantErr {
			if err == nil {
				t.Errorf("ParseRate(%q) expected error", tt.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseRate(%q) unexpected error: %v", tt.in, err)
			continue
		}
		if got != tt.want {
			t.Errorf("ParseRate(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

// TestRateAliases_AllInS950Range guards against typos in the alias table
// that would silently produce an out-of-range rate.
func TestRateAliases_AllInS950Range(t *testing.T) {
	for name, hz := range RateAliases {
		if !InRange(hz) {
			t.Errorf("alias %q maps to %d Hz, outside S950 range (%d..%d)",
				name, hz, MinSampleRateHz, MaxSampleRateHz)
		}
	}
}

func TestParseChannelMode(t *testing.T) {
	tests := []struct {
		in      string
		want    ChannelMode
		wantErr bool
	}{
		{"", ChannelMix, false},
		{"mix", ChannelMix, false},
		{"MIX", ChannelMix, false},
		{"left", ChannelLeft, false},
		{"L", ChannelLeft, false},
		{"right", ChannelRight, false},
		{"r", ChannelRight, false},
		{"  Right  ", ChannelRight, false},
		{"center", 0, true},
		{"unknown", 0, true},
	}
	for _, tt := range tests {
		got, err := ParseChannelMode(tt.in)
		if tt.wantErr {
			if err == nil {
				t.Errorf("ParseChannelMode(%q) expected error", tt.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseChannelMode(%q) unexpected error: %v", tt.in, err)
		}
		if got != tt.want {
			t.Errorf("ParseChannelMode(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

// writeWAV writes a multi-channel test WAV at the given bit depth.
func writeWAV(t *testing.T, path string, data []int, channels, sampleRate, bitDepth int) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer f.Close()
	enc := wav.NewEncoder(f, sampleRate, bitDepth, channels, 1)
	buf := &audio.IntBuffer{
		Format:         &audio.Format{NumChannels: channels, SampleRate: sampleRate},
		SourceBitDepth: bitDepth,
		Data:           data,
	}
	if err := enc.Write(buf); err != nil {
		t.Fatalf("encode: %v", err)
	}
	if err := enc.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// writeAIFF writes a multi-channel test AIFF at the given bit depth.
func writeAIFF(t *testing.T, path string, data []int, channels, sampleRate, bitDepth int) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer f.Close()
	enc := aiff.NewEncoder(f, sampleRate, bitDepth, channels)
	buf := &audio.IntBuffer{
		Format:         &audio.Format{NumChannels: channels, SampleRate: sampleRate},
		SourceBitDepth: bitDepth,
		Data:           data,
	}
	if err := enc.Write(buf); err != nil {
		t.Fatalf("encode: %v", err)
	}
	if err := enc.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestLoadAudio_WAV16BitMono(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mono16.wav")
	data := make([]int, 500)
	for i := range data {
		data[i] = i*32 - 8000
	}
	writeWAV(t, path, data, 1, 22050, 16)

	a, err := LoadAudio(path, ChannelMix)
	if err != nil {
		t.Fatalf("LoadAudio: %v", err)
	}
	if a.SampleRate != 22050 {
		t.Errorf("rate %d, want 22050", a.SampleRate)
	}
	if len(a.PCM) != len(data) {
		t.Fatalf("frames %d, want %d", len(a.PCM), len(data))
	}
	for i, v := range data {
		if int(a.PCM[i]) != v {
			t.Fatalf("PCM[%d] = %d, want %d", i, a.PCM[i], v)
		}
	}
}

func TestLoadAudio_WAVStereo_ChannelModes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stereo16.wav")
	// 4 frames, stereo: L = 100, 200, 300, 400; R = -100, -200, -300, -400.
	data := []int{100, -100, 200, -200, 300, -300, 400, -400}
	writeWAV(t, path, data, 2, 22050, 16)

	left, _ := LoadAudio(path, ChannelLeft)
	if want := []int16{100, 200, 300, 400}; !int16Equal(left.PCM, want) {
		t.Errorf("left = %v, want %v", left.PCM, want)
	}

	right, _ := LoadAudio(path, ChannelRight)
	if want := []int16{-100, -200, -300, -400}; !int16Equal(right.PCM, want) {
		t.Errorf("right = %v, want %v", right.PCM, want)
	}

	mix, _ := LoadAudio(path, ChannelMix)
	if want := []int16{0, 0, 0, 0}; !int16Equal(mix.PCM, want) {
		t.Errorf("mix = %v, want %v", mix.PCM, want)
	}
}

func TestLoadAudio_AIFF16BitMono(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mono16.aiff")
	data := make([]int, 250)
	for i := range data {
		data[i] = i*64 - 8000
	}
	writeAIFF(t, path, data, 1, 44100, 16)

	a, err := LoadAudio(path, ChannelMix)
	if err != nil {
		t.Fatalf("LoadAudio: %v", err)
	}
	if a.SampleRate != 44100 {
		t.Errorf("rate %d, want 44100", a.SampleRate)
	}
	if len(a.PCM) != len(data) {
		t.Fatalf("frames %d, want %d", len(a.PCM), len(data))
	}
	for i, v := range data {
		if int(a.PCM[i]) != v {
			t.Fatalf("PCM[%d] = %d, want %d", i, a.PCM[i], v)
		}
	}
}

func TestLoadAudio_AIFFStereo_ChannelModes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stereo16.aif")
	data := []int{500, -500, 1000, -1000, 1500, -1500}
	writeAIFF(t, path, data, 2, 44100, 16)

	left, err := LoadAudio(path, ChannelLeft)
	if err != nil {
		t.Fatalf("LoadAudio left: %v", err)
	}
	if want := []int16{500, 1000, 1500}; !int16Equal(left.PCM, want) {
		t.Errorf("left = %v, want %v", left.PCM, want)
	}

	right, _ := LoadAudio(path, ChannelRight)
	if want := []int16{-500, -1000, -1500}; !int16Equal(right.PCM, want) {
		t.Errorf("right = %v, want %v", right.PCM, want)
	}
}

// writeFloat32WAV writes a minimal 32-bit IEEE-float WAV by hand so we can
// exercise the float path without relying on go-audio/wav's encoder (which
// has the round-trip bug we're working around in the loader).
func writeFloat32WAV(t *testing.T, path string, samples []float32, channels, sampleRate int) {
	t.Helper()
	dataBytes := uint32(len(samples) * 4)
	byteRate := uint32(sampleRate * channels * 4)
	blockAlign := uint16(channels * 4)
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer f.Close()
	w := func(v interface{}) {
		if err := binary.Write(f, binary.LittleEndian, v); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	// RIFF header.
	f.Write([]byte("RIFF"))
	w(uint32(36 + dataBytes))
	f.Write([]byte("WAVE"))
	// fmt chunk.
	f.Write([]byte("fmt "))
	w(uint32(16))
	w(uint16(0x0003)) // IEEE float
	w(uint16(channels))
	w(uint32(sampleRate))
	w(byteRate)
	w(blockAlign)
	w(uint16(32)) // bits per sample
	// data chunk.
	f.Write([]byte("data"))
	w(dataBytes)
	for _, s := range samples {
		w(s)
	}
}

func TestLoadAudio_WAVFloat32Mono(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "float.wav")
	// Build a recognizable ramp: -1.0 → +1.0 across 5 samples.
	src := []float32{-1.0, -0.5, 0.0, 0.5, 1.0}
	writeFloat32WAV(t, path, src, 1, 44100)

	a, err := LoadAudio(path, ChannelMix)
	if err != nil {
		t.Fatalf("LoadAudio: %v", err)
	}
	if a.SampleRate != 44100 {
		t.Errorf("rate %d, want 44100", a.SampleRate)
	}
	if len(a.PCM) != 5 {
		t.Fatalf("frames %d, want 5", len(a.PCM))
	}
	// Expected: -1.0→-32767, -0.5→-16383ish, 0→0, 0.5→16383ish, 1.0→32767.
	wantApprox := []int16{-32767, -16384, 0, 16384, 32767}
	for i, w := range wantApprox {
		// Allow off-by-one from rounding.
		diff := int(a.PCM[i]) - int(w)
		if diff < 0 {
			diff = -diff
		}
		if diff > 1 {
			t.Errorf("PCM[%d] = %d, want ~%d", i, a.PCM[i], w)
		}
	}
}

func TestLoadAudio_WAVFloat32Stereo_ChannelModes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stereo-float.wav")
	src := []float32{
		1.0, -1.0, // frame 0: L=+1, R=-1
		0.5, -0.5, // frame 1
		0.25, -0.25,
	}
	writeFloat32WAV(t, path, src, 2, 48000)

	left, err := LoadAudio(path, ChannelLeft)
	if err != nil {
		t.Fatalf("LoadAudio left: %v", err)
	}
	if len(left.PCM) != 3 {
		t.Fatalf("left frames %d, want 3", len(left.PCM))
	}
	if left.PCM[0] < 32000 || left.PCM[0] > 32767 {
		t.Errorf("left[0] = %d, want ~32767", left.PCM[0])
	}

	right, _ := LoadAudio(path, ChannelRight)
	if right.PCM[0] > -32000 || right.PCM[0] < -32768 {
		t.Errorf("right[0] = %d, want ~-32767", right.PCM[0])
	}

	mix, _ := LoadAudio(path, ChannelMix)
	// L=+1 + R=-1 averaged = 0.
	if mix.PCM[0] < -1 || mix.PCM[0] > 1 {
		t.Errorf("mix[0] = %d, want ~0", mix.PCM[0])
	}
}

// TestLoadAudio_WAVFloat32_ClampsOutOfRange ensures DAW bounces with peaks
// just over [-1, 1] don't wrap or explode through the int conversion.
func TestLoadAudio_WAVFloat32_ClampsOutOfRange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hot-float.wav")
	src := []float32{1.5, -1.5, float32(math.Inf(1)), float32(math.Inf(-1))}
	writeFloat32WAV(t, path, src, 1, 44100)

	a, err := LoadAudio(path, ChannelMix)
	if err != nil {
		t.Fatalf("LoadAudio: %v", err)
	}
	wantApprox := []int16{32767, -32767, 32767, -32767}
	for i, want := range wantApprox {
		if a.PCM[i] != want {
			t.Errorf("PCM[%d] = %d, want %d", i, a.PCM[i], want)
		}
	}
}

func TestLoadAudio_UnsupportedExtension(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.mp3")
	if err := os.WriteFile(path, []byte("not audio"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadAudio(path, ChannelMix); err == nil {
		t.Error("expected error on unsupported extension")
	}
}

func TestInRange(t *testing.T) {
	if !InRange(22050) {
		t.Error("22050 should be in range")
	}
	if !InRange(MinSampleRateHz) {
		t.Errorf("%d (min) should be in range", MinSampleRateHz)
	}
	if !InRange(MaxSampleRateHz) {
		t.Errorf("%d (max) should be in range", MaxSampleRateHz)
	}
	if InRange(1000) {
		t.Error("1000 should be out of range (below min)")
	}
	if InRange(96000) {
		t.Error("96000 should be out of range (above max)")
	}
}

func TestResample_PassthroughWhenInRange(t *testing.T) {
	in := &LoadedAudio{PCM: []int16{1, 2, 3}, SampleRate: 22050}
	out := ResampleToS950Range(in, 44100)
	if out != in {
		t.Error("expected passthrough (same pointer) when input already in range")
	}
}

// TestResampleTo_ExplicitLoFi covers the "deliberate lo-fi" path: a 44.1 kHz
// in-range source is force-resampled DOWN to 10 kHz for S950 character. This
// is the iconic use case (SP/MPC-style hip-hop sample storage).
func TestResampleTo_ExplicitLoFi(t *testing.T) {
	src := make([]int16, 4410) // 100 ms at 44.1 kHz
	for i := range src {
		src[i] = int16(i * 5)
	}
	in := &LoadedAudio{PCM: src, SampleRate: 44100}
	out := ResampleTo(in, 10000)
	if out.SampleRate != 10000 {
		t.Errorf("rate %d, want 10000", out.SampleRate)
	}
	// 100 ms at 10 kHz is 1000 frames (give or take rounding).
	if len(out.PCM) < 999 || len(out.PCM) > 1001 {
		t.Errorf("len %d, want ~1000", len(out.PCM))
	}
}

func TestResampleTo_PassthroughWhenTargetMatches(t *testing.T) {
	in := &LoadedAudio{PCM: []int16{1, 2, 3}, SampleRate: 22050}
	out := ResampleTo(in, 22050)
	if out != in {
		t.Error("expected passthrough when target rate == source rate")
	}
}

func TestResampleTo_ClampsOutOfRangeTarget(t *testing.T) {
	in := &LoadedAudio{PCM: make([]int16, 100), SampleRate: 22050}
	out := ResampleTo(in, 100) // way below MinSampleRateHz
	if !InRange(out.SampleRate) {
		t.Errorf("out rate %d not clamped into range", out.SampleRate)
	}
	if out.SampleRate != MinSampleRateHz {
		t.Errorf("expected clamp to min %d, got %d", MinSampleRateHz, out.SampleRate)
	}
}

func TestResample_HighRateDownsamples(t *testing.T) {
	// 96 kHz source linear ramp, target 48 kHz. Output should be half-length.
	src := make([]int16, 200)
	for i := range src {
		src[i] = int16(i * 100)
	}
	in := &LoadedAudio{PCM: src, SampleRate: 96000}
	out := ResampleToS950Range(in, 48000)
	if out.SampleRate != 48000 {
		t.Errorf("rate %d, want 48000", out.SampleRate)
	}
	wantLen := 100
	if len(out.PCM) < wantLen-1 || len(out.PCM) > wantLen+1 {
		t.Errorf("len %d, want ~%d", len(out.PCM), wantLen)
	}
}

func TestResample_LowRateClampsToMin(t *testing.T) {
	// 1 kHz source — below MinSampleRateHz. Should be clamped & resampled up.
	in := &LoadedAudio{PCM: make([]int16, 100), SampleRate: 1000}
	out := ResampleToS950Range(in, 0) // 0 -> default 44100, then clamped if needed
	if !InRange(out.SampleRate) {
		t.Errorf("out rate %d not in S950 range", out.SampleRate)
	}
}

func TestLinearResample_KnownVectors(t *testing.T) {
	// Downsample 2x.
	src := []int16{0, 100, 200, 300, 400, 500}
	out := linearResample(src, 48000, 24000)
	// Expected length is ~3.
	if len(out) < 2 || len(out) > 4 {
		t.Errorf("len = %d, want ~3 (got %v)", len(out), out)
	}
	// Upsample 2x.
	out2 := linearResample(src, 22050, 44100)
	if len(out2) < 10 || len(out2) > 14 {
		t.Errorf("upsample len = %d, want ~12 (got %v)", len(out2), out2)
	}
}

func TestScaleToInt16_BitDepths(t *testing.T) {
	tests := []struct {
		v, bits int
		want    int32
	}{
		{0, 16, 0},
		{32767, 16, 32767},
		{-32768, 16, -32768},
		// 8-bit signed (after WAV unsigned-to-signed translation):
		// signed 0 -> 0, signed +127 -> 32512, signed -128 -> -32768.
		{0, 8, 0},
		{127, 8, 32512},
		{-128, 8, -32768},
		// 24-bit signed: full negative (most-negative 24-bit value = 0x800000)
		// maps to -32768 in 16-bit; max positive 0x7FFFFF maps to 32767.
		{0, 24, 0},
		{0x7FFFFF, 24, 32767},
		{0x800000, 24, -32768},
	}
	for _, tt := range tests {
		if got := scaleToInt16(tt.v, tt.bits); got != tt.want {
			t.Errorf("scaleToInt16(%d, %d) = %d, want %d", tt.v, tt.bits, got, tt.want)
		}
	}
}

func int16Equal(a, b []int16) bool {
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
