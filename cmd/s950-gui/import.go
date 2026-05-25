package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/bivers/s950/internal/sample"
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// ImportSample opens a native file picker, loads the chosen WAV/AIFF,
// folds to mono, clamps the sample rate into the S950's supported
// range, and returns both the int16 PCM (for Web Audio preview) and
// the 12-bit offset-binary words (for upload).
//
// If `path` is non-empty the dialog is skipped — useful when the
// frontend wants to import a path it already has (e.g. drag-and-drop
// when we wire that up). An empty string with no error means the
// user cancelled the dialog.
func (a *App) ImportSample(path string) (*ImportInfo, error) {
	if path == "" {
		picked, err := wruntime.OpenFileDialog(a.ctx, wruntime.OpenDialogOptions{
			Title: "Import audio for sampling",
			Filters: []wruntime.FileFilter{
				{DisplayName: "Audio files (.wav, .aiff)", Pattern: "*.wav;*.aif;*.aiff"},
			},
		})
		if err != nil {
			return nil, fmt.Errorf("file dialog: %w", err)
		}
		if picked == "" {
			return nil, nil // user cancelled
		}
		path = picked
	}

	// LoadAudio handles WAV + AIFF, channel folding, and 16-bit
	// normalisation. ChannelMix is the safest default — averages all
	// channels rather than dropping phase information.
	loaded, err := sample.LoadAudio(path, sample.ChannelMix)
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", filepath.Base(path), err)
	}

	// Clamp rate to the S950's window (~2 kHz..65.5 kHz). If the
	// source rate is already in range it's passed through unchanged.
	// Fallback 26040 Hz when the source rate is so far out of range
	// the resampler can't recover meaningfully.
	loaded = sample.ResampleToS950Range(loaded, 26040)

	// PCM → 12-bit offset-binary words.
	words := make([]uint16, len(loaded.PCM))
	for i, s := range loaded.PCM {
		words[i] = sample.PCM16toSW(s)
	}

	return &ImportInfo{
		Path:   path,
		Name:   suggestName(path),
		Rate:   loaded.SampleRate,
		Length: uint32(len(words)),
		PCM:    loaded.PCM,
		Words:  words,
	}, nil
}

// suggestName turns a filesystem path into a candidate SPRM name —
// the S950 allows 10 ASCII chars, uppercase preferred. Strips the
// extension, replaces non-ASCII with underscores, and uppercases.
func suggestName(path string) string {
	base := filepath.Base(path)
	if dot := strings.LastIndex(base, "."); dot > 0 {
		base = base[:dot]
	}
	var b strings.Builder
	for _, r := range strings.ToUpper(base) {
		if b.Len() == 10 {
			break
		}
		if r >= 0x20 && r < 0x7F {
			b.WriteRune(r)
		}
	}
	return b.String()
}
