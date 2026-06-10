package main

import (
	"fmt"
	"time"

	"github.com/bivers/s950/internal/sample"
)

// CopiedAudio is what CopySampleAudio returns: the device-side audio
// words plus the rate the SDS dump header reports for them (derived
// from the dump's PeriodNS). The S950 samples/plays 7.5–48 kHz
// (continuously variable, per the Operator's Manual — 40 kHz was the
// S900's ceiling) and normally stores the rate it was told verbatim,
// so this usually matches the SPRM's SampleRateHz. Carrying the
// dump-header rate is defense-in-depth: it's the field bound to the
// audio bytes themselves, so if the two ever diverge (third-party
// floppy tools, firmware quirks), the host previews at the rate the
// dump claims for the words it actually delivered.
type CopiedAudio struct {
	Words        []uint16 `json:"words"`
	SampleRateHz uint32   `json:"sampleRateHz"`
}

// Phase 1C "Copy from S950": user-initiated pull of a sample's audio
// from the device for samples we didn't upload ourselves (factory
// programs, front-panel-recorded sounds, anything that's on the S950
// but missing host-side PCM). Distinct from Phase 1A/1B which only
// cover audio that originated on the host. Optional, never automatic
// — the user has to click "Copy from S950" on a device-sourced row.

// copySampleTimeout bounds a single CopySampleAudio call. The S950
// can stream ~3 KB/s at its 31.25 kbaud MIDI line rate; a max-size
// sample (~475K words / ~950 KB on the wire) takes ~5 minutes, so
// 10 gives generous headroom for line-rate variability + driver
// buffering.
const copySampleTimeout = 10 * time.Minute

// CopySampleAudio fetches sample-dump audio for the given device slot
// and writes it through the on-disk waveform cache so the next
// session can hydrate without a re-fetch. Returns the words to the
// frontend so it can immediately attach to the live samples store
// without waiting for a separate hydrate roundtrip.
//
// User-initiated only — bound to the "Copy from S950" button. Never
// auto-invoked because SDATA dumps are slow (seconds to minutes) and
// the user should opt in to that wire traffic.
func (a *App) CopySampleAudio(slot int) (*CopiedAudio, error) {
	if slot < 0 || slot > 99 {
		return nil, fmt.Errorf("slot %d out of range (0..99)", slot)
	}

	d, err := a.lockDevice()
	if err != nil {
		return nil, err
	}
	defer a.mu.Unlock()

	hdr, words, err := d.GetSampleAudio(byte(slot), copySampleTimeout)
	if err != nil {
		return nil, fmt.Errorf("copy sample %d: %w", slot, err)
	}
	// Sanity check: the header's TotalWords must agree with the
	// length of the words slice the parser returned (post-padding
	// trim). Mismatch here would be a parser bug — defend against it
	// before we write a bad cache entry.
	if uint32(len(words)) != hdr.TotalWords {
		return nil, fmt.Errorf("internal: word count mismatch (%d body vs %d header)",
			len(words), hdr.TotalWords)
	}

	// The frontend persists into the Phase 1B cache via the standard
	// PutCachedWaveform binding once it has the words in hand — it
	// already knows the sample's SPRM name from refreshCatalog. We
	// also return the dump header's playback rate so the host
	// preview plays the audio at its real rate, not the SPRM-claimed
	// rate which can lag the device's internal resampling.
	return &CopiedAudio{
		Words:        words,
		SampleRateHz: sample.PeriodNSToHz(hdr.PeriodNS),
	}, nil
}
