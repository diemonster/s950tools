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

	if err := d.T.Send(noteOnBytes(note, velocity, channel)); err != nil {
		return fmt.Errorf("send Note On: %w", err)
	}
	time.Sleep(time.Duration(durationMs) * time.Millisecond)
	if err := d.T.Send(noteOffBytes(note, channel)); err != nil {
		return fmt.Errorf("send Note Off: %w", err)
	}
	return nil
}

// ActivateProgram sends a MIDI Program Change so the S950 loads the
// program mapped to `midiProgNumber` (each program's MIDI prog # —
// NOT its slot). Works over both transports: DIN in MIDI mode,
// channel-messages-over-serial in RS-232 mode (hardware-verified).
//
// Fire-and-forget by hardware design: the S950 has no mechanism to
// report its active program (no SysEx primitive; transmits nothing
// on front-panel changes), so this can COMMAND a switch but never
// confirm one. The frontend gates calls to provably-unambiguous
// cases (unique prog #, RESPOND TO PC on, device-side program) and
// reports the action, never a "synced" state.
func (a *App) ActivateProgram(midiProgNumber int, channel int) error {
	if midiProgNumber < 0 || midiProgNumber > 127 {
		return fmt.Errorf("MIDI program number %d out of range (0..127)", midiProgNumber)
	}
	d, err := a.lockDevice()
	if err != nil {
		return err
	}
	defer a.mu.Unlock()
	pc := []byte{0xC0 | byte(channel&0x0F), byte(midiProgNumber & 0x7F)}
	if err := d.T.Send(pc); err != nil {
		return fmt.Errorf("send program change: %w", err)
	}
	return nil
}

// noteOnBytes returns the 3-byte MIDI Note-On message for the given
// note/velocity/channel. Pure helper — splits out the byte-shaping
// from PreviewMidi so the clamp semantics are unit-testable without
// a transport.
func noteOnBytes(note, velocity, channel int) []byte {
	return []byte{0x90 | byte(channel&0x0F), byte(note & 0x7F), byte(velocity & 0x7F)}
}

// noteOffBytes returns the 3-byte MIDI Note-Off message. Pure helper.
func noteOffBytes(note, channel int) []byte {
	return []byte{0x80 | byte(channel&0x0F), byte(note & 0x7F), 0x00}
}
