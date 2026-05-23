package sample

import (
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

func TestPCM16toSWKnownVectors(t *testing.T) {
	tests := []struct {
		pcm    int16
		wantSW uint16
	}{
		{0, 2048},        // silence -> 0x800
		{32767, 4095},    // positive clip
		{-32768, 0},      // negative clip
		{16, 2049},       // smallest +1 step (>> 4)
		{-16, 2047},      // smallest -1 step
	}
	for _, tt := range tests {
		got := PCM16toSW(tt.pcm)
		if got != tt.wantSW {
			t.Errorf("PCM16toSW(%d) = %d, want %d", tt.pcm, got, tt.wantSW)
		}
	}
}

func TestSWtoPCM16(t *testing.T) {
	if got := SWtoPCM16(0x800); got != 0 {
		t.Errorf("SWtoPCM16(0x800) = %d, want 0", got)
	}
	if got := SWtoPCM16(4095); got != int16(2047<<4) {
		t.Errorf("SWtoPCM16(4095) = %d, want %d", got, int16(2047<<4))
	}
	if got := SWtoPCM16(0); got != int16(-2048<<4) {
		t.Errorf("SWtoPCM16(0) = %d, want %d", got, int16(-2048<<4))
	}
}

func TestPCM16SWQuantization(t *testing.T) {
	// Round-trip a random PCM16 through SW. The result is the original truncated
	// to 12 bits (low nybble cleared by the >>4 in PCM16toSW).
	r := rand.New(rand.NewSource(1))
	for i := 0; i < 1000; i++ {
		s := int16(r.Uint32())
		got := SWtoPCM16(PCM16toSW(s))
		want := int16(int32(s)>>4) << 4
		// Edge: +32767 saturates to +32752 because 4095<<4 - 32768 = 32752.
		// Verify via direct compute, not by reapplying the function.
		if s == 32767 {
			want = int16(2047 << 4) // 32752
		}
		if got != want {
			t.Errorf("round trip %d -> %d, want %d", s, got, want)
		}
	}
}

func TestHzToPeriodNS(t *testing.T) {
	tests := []struct {
		hz   uint32
		ok   bool
		want uint32
	}{
		{22050, true, 45351}, // round(1e9/22050) = 45351
		{44100, true, 22676},
		{32000, true, 31250},
		{1000, false, 0},   // too low
		{100000, false, 0}, // too high
	}
	for _, tt := range tests {
		got, err := HzToPeriodNS(tt.hz)
		if tt.ok && err != nil {
			t.Errorf("HzToPeriodNS(%d) unexpected error: %v", tt.hz, err)
		}
		if !tt.ok && err == nil {
			t.Errorf("HzToPeriodNS(%d) expected error", tt.hz)
		}
		if tt.ok && got != tt.want {
			t.Errorf("HzToPeriodNS(%d) = %d, want %d", tt.hz, got, tt.want)
		}
	}
}

func TestPeriodNSToHz(t *testing.T) {
	if got := PeriodNSToHz(45351); got != 22050 {
		t.Errorf("got %d, want 22050", got)
	}
	if got := PeriodNSToHz(0); got != 0 {
		t.Errorf("got %d, want 0", got)
	}
}

func TestPCM16ToWordsLengthCheck(t *testing.T) {
	short := make([]int16, 50)
	if _, err := PCM16ToWords(short); err != ErrLengthTooShort {
		t.Errorf("short: got %v, want ErrLengthTooShort", err)
	}
	long := make([]int16, MaxTotalWords+1)
	if _, err := PCM16ToWords(long); err != ErrLengthTooLong {
		t.Errorf("long: got %v, want ErrLengthTooLong", err)
	}
	ok := make([]int16, MinTotalWords)
	if _, err := PCM16ToWords(ok); err != nil {
		t.Errorf("ok length: got %v, want nil", err)
	}
}

func TestWAVRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.wav")

	// Build a 22050 Hz sine-ish signal (just incrementing ramp).
	pcm := make([]int16, 1000)
	for i := range pcm {
		pcm[i] = int16(i*32 - 16000)
	}
	if err := SaveWAV(path, pcm, 22050); err != nil {
		t.Fatalf("SaveWAV: %v", err)
	}
	// Re-load.
	w, err := LoadWAV(path)
	if err != nil {
		t.Fatalf("LoadWAV: %v", err)
	}
	if w.SampleRate != 22050 {
		t.Errorf("sample rate: got %d, want 22050", w.SampleRate)
	}
	if len(w.PCM) != len(pcm) {
		t.Fatalf("PCM len: got %d, want %d", len(w.PCM), len(pcm))
	}
	for i := range pcm {
		if w.PCM[i] != pcm[i] {
			t.Fatalf("PCM[%d] = %d, want %d", i, w.PCM[i], pcm[i])
		}
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("WAV file vanished: %v", err)
	}
}
