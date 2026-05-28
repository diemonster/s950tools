// Per-sample SDATA + SPRM upload — wires the "Send to S950" button
// on the Sample tab. Pairs with `Copy from S950` (Phase 1C) for the
// opposite direction: this binding takes host-side audio + the
// edited high-level parameters and pushes both to the device in one
// orchestration. Distinct from Apply Slicing's bundled multi-sample
// flow — that's still the right path when you're committing a
// sliced parent into N children at once.

package main

import (
	"fmt"
	"time"

	"github.com/bivers/s950/internal/device"
	"github.com/bivers/s950/internal/protocol"
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// sendSampleNAKWindow is how long we wait after the dump's wire-time
// drain before checking the inbound queue for NAKs. Most NAKs land
// near-immediately after the offending block, but the device's
// internal accounting can lag by a few hundred ms. 500ms is the
// same value the CLI's put-sample flow uses.
const sendSampleNAKWindow = 500 * time.Millisecond

// SendSampleProgress is the payload of the "sendsample:progress"
// Wails event. The frontend's progress modal subscribes to this so
// the user sees what phase the upload is in (synthetic but accurate:
// the wire-time drain is deterministic from byte count + baud).
type SendSampleProgress struct {
	Phase   string `json:"phase"`   // "uploading" | "waiting_drain" | "writing_sprm" | "done" | "error"
	Slot    int    `json:"slot"`
	Sent    int    `json:"sent"`    // bytes queued on the wire
	DrainMs int    `json:"drainMs"` // expected drain milliseconds
	Message string `json:"message"`
}

// SendSample uploads `words` to device slot `slot` and writes back
// the high-level SampleParams as an SPRM update. The two steps must
// run on the same locked transport — otherwise concurrent Wails
// calls could interleave SysEx between the SDATA dump and the SPRM
// write, corrupting either.
//
// The frontend builds the SampleParams from the edited Sample
// fields (start, end, loop, mode, tune, loudness, etc.) and passes
// it through. Backend trusts the shape — validation is the
// frontend's responsibility.
func (a *App) SendSample(slot int, words []uint16, sampleRateHz uint32, params protocol.SampleParams) error {
	if slot < 0 || slot > 99 {
		return fmt.Errorf("slot %d out of range (0..99)", slot)
	}
	if len(words) == 0 {
		return fmt.Errorf("no audio data — sample has no host-side words")
	}

	d, err := a.lockDevice()
	if err != nil {
		return err
	}
	defer a.mu.Unlock()

	a.emitSendProgress(SendSampleProgress{
		Phase: "uploading", Slot: slot,
		Message: fmt.Sprintf("Uploading %d words to slot %d…", len(words), slot),
	})

	// Open-loop SDATA dump. PutSampleOpenLoop queues the full
	// envelope onto the OS MIDI buffer in one shot; we then wait
	// the expected drain time so subsequent SysEx (the SPRM write)
	// doesn't race against bytes still in flight.
	sent, drain, err := d.PutSampleOpenLoop(words, device.PutSampleOpts{
		Num:          byte(slot),
		SampleRateHz: sampleRateHz,
		LoopStart:    params.Start,
		LoopEnd:      params.End,
		Mode:         params.ReplayMode - 'O',
	})
	if err != nil {
		a.emitSendProgress(SendSampleProgress{Phase: "error", Slot: slot, Message: err.Error()})
		return fmt.Errorf("upload SDATA: %w", err)
	}

	a.emitSendProgress(SendSampleProgress{
		Phase: "waiting_drain", Slot: slot, Sent: sent, DrainMs: int(drain.Milliseconds()),
		Message: fmt.Sprintf("Queued %d bytes; draining (%s)…", sent, drain.Round(100*time.Millisecond)),
	})
	time.Sleep(drain)

	// NAK check before SPRM: if the dump itself was rejected, the
	// SPRM write would land on a half-populated slot. Surface that
	// to the user before they assume the upload succeeded.
	if naks := d.CollectNAKs(sendSampleNAKWindow); naks > 0 {
		msg := fmt.Sprintf("%d NAK%s during upload — sample may be truncated; please retry",
			naks, plural(naks))
		a.emitSendProgress(SendSampleProgress{Phase: "error", Slot: slot, Message: msg})
		return fmt.Errorf("%s", msg)
	}

	a.emitSendProgress(SendSampleProgress{
		Phase: "writing_sprm", Slot: slot, Message: "Writing sample parameters…",
	})
	if err := d.SetParams(byte(slot), &params); err != nil {
		a.emitSendProgress(SendSampleProgress{Phase: "error", Slot: slot, Message: err.Error()})
		return fmt.Errorf("write SPRM: %w", err)
	}

	a.emitSendProgress(SendSampleProgress{
		Phase: "done", Slot: slot, Sent: sent,
		Message: fmt.Sprintf("Uploaded %d words to slot %d", len(words), slot),
	})
	return nil
}

func (a *App) emitSendProgress(p SendSampleProgress) {
	wruntime.EventsEmit(a.ctx, "sendsample:progress", p)
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
