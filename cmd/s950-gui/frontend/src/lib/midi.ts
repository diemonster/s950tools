// MIDI ↔ display-name helpers. The S950 keyboard layout in the canvas
// is the 88-key piano range (MIDI 21..108, A0..C8), but keygroup
// boundaries can be anywhere in 0..127 — the canvas just clips.

const NAMES = ['C', 'C#', 'D', 'D#', 'E', 'F', 'F#', 'G', 'G#', 'A', 'A#', 'B'];

// noteName(60) → "C3" (using the Yamaha/Akai convention: MIDI 60 = C3).
export function noteName(midi: number): string {
  const octave = Math.floor(midi / 12) - 2;
  return NAMES[midi % 12] + octave;
}

// rangeLabel(36, 36)  → "C2"
// rangeLabel(53, 55)  → "F3 — G3"
export function rangeLabel(lo: number, hi: number): string {
  return lo === hi ? noteName(lo) : `${noteName(lo)} — ${noteName(hi)}`;
}

// Canvas X coordinate (as % of the 88-key piano strip) for a MIDI key.
// MIDI 21 (A0) is at 0%, MIDI 108 (C8) is at 100%.
const STRIP_LO = 21;
const STRIP_KEYS = 88;
const STRIP_STEP = 100 / STRIP_KEYS;

export function midiX(midi: number): number {
  return (midi - STRIP_LO) * STRIP_STEP;
}

// Width across [lo..hi] (inclusive) in strip percent.
export function midiW(lo: number, hi: number): number {
  return (hi - lo + 1) * STRIP_STEP;
}
