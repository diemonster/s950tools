// Pre-upload resample — wires the Sample-tab "Rate" dropdown so a
// user can downsample an imported WAV to the S950's classic lo-fi
// rates (SP1200's 26040, telephone's 8000, etc.) before Send to
// S950. Mirrors the CLI's `--rate` flag: actually changes the
// audio bytes, distinct from tune (which only modifies SPRM
// metadata).

package main

import (
	"fmt"

	"github.com/bivers/s950/internal/sample"
)

// ResampledSample is the payload returned to the frontend after a
// rate change. The frontend uses it to replace the sample's PCM /
// words12 / rate / length in the host store; nothing is sent to
// the device until the user clicks Send to S950.
type ResampledSample struct {
	// PCM is the int16 PCM at the new rate, for Web Audio preview.
	PCM []int16 `json:"pcm"`
	// Words is the same audio as 12-bit S950 offset-binary words,
	// ready for PutSampleOpenLoop.
	Words []uint16 `json:"words"`
	// Rate is the actual resampled rate. Equals toRate unless that
	// fell outside the S950's window (~2k..65k Hz), in which case
	// the value is clamped — surfaces to the UI so the dropdown
	// can update its displayed value to match reality.
	Rate uint32 `json:"rate"`
	// Length is the new word count after resampling (input PCM
	// length × toRate / fromRate, rounded). The frontend uses this
	// to update the sample's length field + scale markers.
	Length uint32 `json:"length"`
}

// ResampleSample takes the host-side int16 PCM of a sample, runs
// it through the resampler at toRate, and returns the new PCM +
// 12-bit S950 words. Pure host-side operation — no device round
// trip. The frontend calls this after the user picks a different
// rate from the Sample-tab dropdown; the actual upload to the
// device happens later via Send to S950 with the new audio.
func (a *App) ResampleSample(pcm []int16, fromRate, toRate uint32) (*ResampledSample, error) {
	if len(pcm) == 0 {
		return nil, fmt.Errorf("ResampleSample: pcm is empty")
	}
	if fromRate == 0 {
		return nil, fmt.Errorf("ResampleSample: fromRate is zero")
	}
	if toRate == 0 {
		return nil, fmt.Errorf("ResampleSample: toRate is zero")
	}

	in := &sample.LoadedAudio{
		PCM:        pcm,
		SampleRate: fromRate,
	}
	out := sample.ResampleTo(in, toRate)

	words := make([]uint16, len(out.PCM))
	for i, s := range out.PCM {
		words[i] = sample.PCM16toSW(s)
	}
	return &ResampledSample{
		PCM:    out.PCM,
		Words:  words,
		Rate:   out.SampleRate,
		Length: uint32(len(out.PCM)),
	}, nil
}
