// MIDI preview — sends a Note On / Note Off pair to the device so
// the user can hear what a sample sounds like via the S950's own
// audio path. Only useful when a program currently maps the
// triggered note to the sample (documented constraint; we don't
// auto-create preview programs because the device's per-write
// latency makes that flow sluggish).

package main

import (
	"fmt"
	"time"
)

// previewMaxDuration caps the held-note time. Even if the caller
// passes a huge durationMs the device won't sit holding a note for
// a full minute — the play button is meant for snappy auditioning,
// not sustained playback. 30s is plenty for any sample on the
// S950 (its memory ceiling is ~3.5s at 48 kHz mono).
const previewMaxDuration = 30 * time.Second

// PreviewMidi sends a MIDI Note On at the given note + velocity on
// the given channel, then sleeps durationMs and sends Note Off.
// Synchronous on purpose: the wire mutex is held the whole time so
// other Wails calls queue behind it (matches how we serialise
// SysEx — keeps the device's input buffer predictable).
//
// note / velocity / channel are 0..127 / 0..127 / 0..15. The
// frontend is responsible for picking sensible values; backend
// only clamps to MIDI's 7-bit byte width.
func (a *App) PreviewMidi(note, velocity, channel, durationMs int) error {
	if durationMs <= 0 {
		return fmt.Errorf("durationMs must be > 0")
	}
	if d := time.Duration(durationMs) * time.Millisecond; d > previewMaxDuration {
		durationMs = int(previewMaxDuration / time.Millisecond)
	}
	d, err := a.lockDevice()
	if err != nil {
		return err
	}
	defer a.mu.Unlock()

	ch := byte(channel & 0x0F)
	n := byte(note & 0x7F)
	v := byte(velocity & 0x7F)

	on := []byte{0x90 | ch, n, v}
	if err := d.T.Send(on); err != nil {
		return fmt.Errorf("send Note On: %w", err)
	}
	time.Sleep(time.Duration(durationMs) * time.Millisecond)
	off := []byte{0x80 | ch, n, 0x00}
	if err := d.T.Send(off); err != nil {
		return fmt.Errorf("send Note Off: %w", err)
	}
	return nil
}
