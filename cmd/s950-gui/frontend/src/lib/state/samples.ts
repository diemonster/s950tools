// Stub sample bank + the cross-tab "selected sample" store. Shared
// between Keygroup (drag onto zone) and Sample (waveform editor).
//
// Will be replaced by App.Catalog + App.GetParams when bindings land.

import { writable, derived, get } from 'svelte/store';

export type Sample = {
  slot: number;
  name: string;
  rate: number;      // Hz, e.g. 26040
  length: number;    // total words
  start: number;     // first replay point (words)
  end: number;       // end point (words)
  loopStart: number; // loop start (words)
  loopLength: number;// loop length (words)
  mode: 'one-shot' | 'loop' | 'ping-pong';
  reverse: boolean;
  velXfade: boolean;
  tune: number;      // semitones + cents
  loudness: number;  // signed
  // Round-trip cache of the original 120-byte SPRM block. Populated
  // by GetSampleParams and passed back on SetSampleParams so the
  // S950's reserved/undocumented bytes survive an edit cycle.
  raw?: number[];
  // ---------- Host-side audio (import-only) ----------
  // pcm: int16 mono PCM at `rate`, used for Web Audio preview.
  // words12: same audio as 12-bit S950 words, used as the slicing
  // source so we don't re-convert on Apply. Both arrays are
  // populated by ImportSample and never touched once stored.
  pcm?: number[];
  words12?: number[];
};

function s(
  slot: number,
  name: string,
  rate: number,
  length: number,
  mode: Sample['mode'] = 'one-shot',
): Sample {
  return {
    slot, name, rate, length, mode,
    start: 0,
    end: length,
    loopStart: Math.floor(length * 0.35),
    loopLength: Math.max(200, Math.floor(length * 0.2)),
    reverse: false,
    velXfade: false,
    tune: 0,
    loudness: 0,
  };
}

const stub: Sample[] = [
  s(0,  'KICK',     26040,  8200),
  s(1,  'SNARE',    26040, 12400, 'loop'),
  s(2,  'RIM',      26040,  3400),
  s(3,  'CLAP',     26040,  9600),
  s(4,  'CONGA',    26040,  7400),
  s(5,  '808 KICK', 26040,  6800),
  s(6,  '808 TOM',  26040,  5200),
  s(7,  '808 HHC',  26040,  2400),
  s(8,  '909 TOM',  26040,  5800),
  s(9,  'CR78 HHO', 26040,  4200),
  s(10, '909 HHO',  26040,  4800),
  s(11, '808 HHO',  26040,  5400),
];

export const samples = writable<Sample[]>(stub);
export const selectedSampleSlot = writable<number>(stub[1].slot); // SNARE — has loop set

function makeSelectedSample() {
  const inner = derived(
    [samples, selectedSampleSlot],
    ([$samples, $selectedSampleSlot]) =>
      $samples.find((s) => s.slot === $selectedSampleSlot) ?? $samples[0]
  );
  return {
    subscribe: inner.subscribe,
    update(patch: Partial<Sample>) {
      const slot = get(selectedSampleSlot);
      samples.update((list) =>
        list.map((x) => (x.slot === slot ? { ...x, ...patch } : x))
      );
      // Schedule SPRM write-back. Lazy-imported to avoid the
      // livesync ↔ samples circular import.
      void import('./livesync').then((m) => m.scheduleSampleWriteback(slot));
    },
  };
}

export const selectedSample = makeSelectedSample();
