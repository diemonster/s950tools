package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/go-audio/audio"
	"github.com/go-audio/wav"
)

// Tests for the path-taking cores extracted from the Wails-dialog
// bindings (importSampleFromPath, writeSampleWav). Each function
// used to be tangled with wruntime.OpenFileDialog / SaveFileDialog;
// now they take a path and can be driven from a tempdir.

// makeTestWAV writes a minimal mono int16 WAV at `rate` for the
// importer test. Returns the path. The PCM is a simple ramp — the
// importer doesn't care about the audio shape, only that LoadAudio
// can parse it.
func makeTestWAV(t *testing.T, rate int, frames int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.wav")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create test wav: %v", err)
	}
	defer f.Close()
	enc := wav.NewEncoder(f, rate, 16, 1, 1)
	data := make([]int, frames)
	for i := range data {
		data[i] = (i * 100) - (frames * 50) // ramp through 0
	}
	buf := &audio.IntBuffer{
		Format:         &audio.Format{NumChannels: 1, SampleRate: rate},
		SourceBitDepth: 16,
		Data:           data,
	}
	if err := enc.Write(buf); err != nil {
		t.Fatalf("encode test wav: %v", err)
	}
	if err := enc.Close(); err != nil {
		t.Fatalf("close test wav: %v", err)
	}
	return path
}

func TestImportSampleFromPath_LoadsAndConvertsInRangeWAV(t *testing.T) {
	// 22050 Hz is inside the S950's ~2k..65k window — should pass
	// through unchanged. importSampleFromPath must populate PCM,
	// Words (same length as PCM), Rate, Length, and Name from path.
	path := makeTestWAV(t, 22050, 500)
	info, err := importSampleFromPath(path)
	if err != nil {
		t.Fatalf("importSampleFromPath: %v", err)
	}
	if info.Rate != 22050 {
		t.Errorf("Rate = %d, want 22050 (in-range source should pass through)", info.Rate)
	}
	if len(info.PCM) == 0 {
		t.Fatal("PCM is empty")
	}
	if len(info.Words) != len(info.PCM) {
		t.Errorf("len(Words) %d != len(PCM) %d", len(info.Words), len(info.PCM))
	}
	if info.Length != uint32(len(info.Words)) {
		t.Errorf("Length %d != len(Words) %d", info.Length, len(info.Words))
	}
	if info.Path != path {
		t.Errorf("Path = %q, want %q", info.Path, path)
	}
	if info.Name != "TEST" {
		t.Errorf("Name = %q, want %q (derived from basename)", info.Name, "TEST")
	}
}

func TestImportSampleFromPath_ErrorsOnMissingFile(t *testing.T) {
	_, err := importSampleFromPath(filepath.Join(t.TempDir(), "missing.wav"))
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}


func TestWriteSampleWav_RoundTripsThroughWavDecoder(t *testing.T) {
	// Write a known PCM via writeSampleWav, decode the file back with
	// the wav library, and confirm sample-rate + frame count + values
	// match. Pins the on-disk format (16-bit mono PCM at `rate`).
	path := filepath.Join(t.TempDir(), "out.wav")
	pcm := []int16{0, 1000, -1000, 32000, -32000, 500}
	if err := writeSampleWav(path, 22050, pcm); err != nil {
		t.Fatalf("writeSampleWav: %v", err)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open written wav: %v", err)
	}
	defer f.Close()
	dec := wav.NewDecoder(f)
	if !dec.IsValidFile() {
		t.Fatal("written WAV is invalid per decoder")
	}
	buf, err := dec.FullPCMBuffer()
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if buf.Format.SampleRate != 22050 {
		t.Errorf("decoded SampleRate = %d, want 22050", buf.Format.SampleRate)
	}
	if buf.Format.NumChannels != 1 {
		t.Errorf("decoded NumChannels = %d, want 1", buf.Format.NumChannels)
	}
	if len(buf.Data) != len(pcm) {
		t.Errorf("decoded frame count = %d, want %d", len(buf.Data), len(pcm))
	}
	for i, want := range pcm {
		if got := int16(buf.Data[i]); got != want {
			t.Errorf("pcm[%d] = %d, want %d", i, got, want)
		}
	}
}

func TestWriteSampleWav_ErrorsOnUncreatablePath(t *testing.T) {
	// Path in a non-existent dir → create fails. Pins that the error
	// is surfaced rather than swallowed (the dialog shell relies on
	// that to propagate "couldn't write" up to the user).
	bad := filepath.Join(t.TempDir(), "does", "not", "exist", "out.wav")
	if err := writeSampleWav(bad, 22050, []int16{1, 2, 3}); err == nil {
		t.Fatal("expected error writing to nonexistent dir")
	}
}
