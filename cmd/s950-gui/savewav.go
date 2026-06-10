// Save .wav — writes a sample's host-side PCM to disk via the OS
// file dialog. The Sample-tab "Download .wav..." button calls this;
// pairs with ImportSample on the opposite side (sample → disk vs
// disk → sample). No wire traffic — pure host op.

package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/go-audio/audio"
	"github.com/go-audio/wav"
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// SaveSampleWav prompts the user for a save path and writes the
// given int16 PCM as a 16-bit mono WAV at the sample's rate.
// Returns the chosen path on success, or "" when the user
// cancelled the dialog.
//
// pcm is the host-side int16 PCM the Sample-tab waveform displays;
// rate is the sample's current rate (post-resample if applicable).
// The S950 only stores 12-bit audio so we lose nothing by writing
// 16-bit — the bottom 4 bits are zero anyway after SWtoPCM16.
func (a *App) SaveSampleWav(name string, rate uint32, pcm []int16) (string, error) {
	if len(pcm) == 0 {
		return "", fmt.Errorf("no audio to save (sample has no host PCM)")
	}
	if rate == 0 {
		return "", fmt.Errorf("invalid sample rate")
	}
	defaultName := name
	if defaultName == "" {
		defaultName = "sample"
	}
	if filepath.Ext(defaultName) == "" {
		defaultName += ".wav"
	}
	path, err := wruntime.SaveFileDialog(a.ctx, wruntime.SaveDialogOptions{
		Title:           "Save sample as WAV",
		DefaultFilename: defaultName,
		Filters: []wruntime.FileFilter{
			{DisplayName: "WAV (.wav)", Pattern: "*.wav"},
		},
	})
	if err != nil {
		return "", fmt.Errorf("save dialog: %w", err)
	}
	if path == "" {
		return "", nil // user cancelled
	}
	if err := writeSampleWav(path, rate, pcm); err != nil {
		return "", err
	}
	return path, nil
}

// writeSampleWav is the path-taking core of SaveSampleWav — writes
// int16 PCM at `rate` as a 16-bit mono WAV to `path`. Split from the
// dialog shell so the encode path is unit-testable.
func writeSampleWav(path string, rate uint32, pcm []int16) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create %s: %w", filepath.Base(path), err)
	}
	defer f.Close()

	enc := wav.NewEncoder(f, int(rate), 16, 1, 1)
	buf := &audio.IntBuffer{
		Format:         &audio.Format{NumChannels: 1, SampleRate: int(rate)},
		SourceBitDepth: 16,
		Data:           make([]int, len(pcm)),
	}
	for i, s := range pcm {
		buf.Data[i] = int(s)
	}
	if err := enc.Write(buf); err != nil {
		return fmt.Errorf("encode WAV: %w", err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("close WAV: %w", err)
	}
	return nil
}
