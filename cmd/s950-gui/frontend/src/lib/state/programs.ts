// Stub program data + the cross-tab "selected program" store.
//
// Stays a frontend-only store for now so the UI can be exercised
// without a live S950. Once App.Catalog / App.GetProgram are wired
// in, this file is the single place to swap in real data — the
// component API (selectedProgram.update(patch), selectedSlot.set(N))
// won't change.

import { writable, derived, get } from 'svelte/store';

// One side of the velocity split. The S950 has soft (vel < switch)
// and loud (vel >= switch) layers per keygroup; each carries its own
// sample binding plus per-sample transpose/filter/loudness trim.
export type Layer = {
  sample: string;       // sample name, '' = layer disabled
  transpose: number;    // semitones + cents (decimal)
  filter: number;       // 0..99
  loudness: number;     // 0..99, signed-ish
};

// Per-keygroup modulation values. Envelopes are ADSR (0..99 each),
// LFO and velocity routing are 0..99 knobs. Filter routing collapses
// the S950's env→VCF + key-tracking knobs.
export type Modulation = {
  ampEnv:    { a: number; d: number; s: number; r: number };
  filterEnv: { a: number; d: number; s: number; r: number };
  envToVCF:  number;
  keyTrack:  number;
  lfo: {
    rate:      number;
    depth:     number;
    fadeIn:    number; // S950 "build time"
    modWheel:  number;
    aftertouch: number;
  };
  vel: { toFilter: number; toLoud: number; toAttack: number; toRelease: number };
  warpAmount: number;
};

export type Keygroup = {
  n: number;
  lowKey: number;   // MIDI 0..127
  highKey: number;  // MIDI 0..127
  vel: number;      // velocity switch (0..128; 128 = soft layer only)
  soft: Layer;
  loud: Layer;
  // CSS custom property reference for the swatch + canvas zone tint.
  color: string;
  midiChannel: number; // 0..15, or 16 for OMNI
  voiceOut: string;    // 'ALL' | '1'..'8'
  oneShot: boolean;
  constPitch: boolean;
  keyFilter: boolean;
  mod: Modulation;
  // Round-trip cache: the 140-byte keygroup wire payload as hex.
  // Populated when we load from the device; passed back on SetProgram
  // so undocumented/reserved bytes the S950 still uses survive.
  rawBytesHex?: string;
};

export type Program = {
  slot: number;
  name: string;
  midiProg: number;
  respondPC: boolean;
  keyTilt: number;
  positionalXfade: boolean;
  keygroups: Keygroup[];
  // Round-trip cache: the 76-byte program-header wire payload as hex.
  // Same purpose as Keygroup.rawBytesHex — see comment there.
  rawHeaderHex?: string;
};

// Default modulation block — same values as examples/program.json's
// new-keygroup defaults so the form looks sane on first render.
function defaultMod(): Modulation {
  return {
    ampEnv:    { a: 0,  d: 80, s: 99, r: 30 },
    filterEnv: { a: 20, d: 20, s: 20, r: 20 },
    envToVCF: 0,
    keyTrack: 50,
    lfo: { rate: 42, depth: 0, fadeIn: 64, modWheel: 50, aftertouch: 0 },
    vel: { toFilter: 10, toLoud: 30, toAttack: 0, toRelease: 0 },
    warpAmount: 0,
  };
}

function kg(
  n: number,
  lowKey: number,
  highKey: number,
  vel: number,
  softSample: string,
  softTranspose: number,
  loudSample: string,
  color: string,
): Keygroup {
  return {
    n, lowKey, highKey, vel, color,
    soft: { sample: softSample, transpose: softTranspose, filter: 99, loudness: 0 },
    loud: { sample: loudSample, transpose: 0,             filter: 99, loudness: 0 },
    midiChannel: 16, // OMNI
    voiceOut: 'ALL',
    oneShot: true,
    constPitch: false,
    keyFilter: false,
    mod: defaultMod(),
  };
}

const stub: Program[] = [
  {
    slot: 2,
    name: 'EX KIT',
    midiProg: 1,
    respondPC: true,
    keyTilt: 0,
    positionalXfade: false,
    keygroups: [
      kg(1, 36, 36, 128, 'KICK',     24, '',           '--rb-yellow'),
      kg(2, 38, 38, 128, 'SNARE',    22, '',           '--rb-magenta'),
      kg(3, 42, 42, 128, 'HHC',      18, '',           '--rb-cyan'),
      kg(4, 46, 46, 128, 'HHO',      14, '',           '--rb-green'),
      kg(5, 48, 48, 128, 'TOM',      12, '',           '--rb-orange'),
      kg(6, 50, 50, 128, '808 KICK', 10, '',           '--rb-red'),
      kg(7, 53, 55, 64,  'CLAP',     0,  'CLAP (loud)', '--rb-purple'),
    ],
  },
  {
    slot: 5,
    name: 'PIANO',
    midiProg: 2,
    respondPC: true,
    keyTilt: 0,
    positionalXfade: true,
    keygroups: [
      kg(1, 21, 35, 128, 'PIANO_LO1', 0, '', '--rb-blue'),
      kg(2, 36, 47, 128, 'PIANO_LO2', 0, '', '--rb-cyan'),
      kg(3, 48, 59, 128, 'PIANO_MID', 0, '', '--rb-green'),
      kg(4, 60, 71, 128, 'PIANO_HI1', 0, '', '--rb-yellow'),
      kg(5, 72, 83, 128, 'PIANO_HI2', 0, '', '--rb-orange'),
      kg(6, 84, 108, 128, 'PIANO_HI3', 0, '', '--rb-red'),
    ],
  },
  {
    slot: 12,
    name: 'BASS',
    midiProg: 3,
    respondPC: false,
    keyTilt: 0,
    positionalXfade: false,
    keygroups: [
      kg(1, 24, 29, 128, 'BASS_LO',  0, '', '--rb-green'),
      kg(2, 30, 35, 128, 'BASS_MID', 0, '', '--rb-yellow'),
      kg(3, 36, 40, 128, 'BASS_HI',  0, '', '--rb-orange'),
      kg(4, 41, 43, 128, 'BASS_TOP', 0, '', '--rb-red'),
    ],
  },
];

export const programs = writable<Program[]>(stub);
export const selectedSlot = writable<number>(stub[0].slot);

// Which keygroup is currently being edited on the Keygroup tab. Set
// when Edit ↗ is clicked on the Program tab; persists across tab
// switches.
export const selectedKeygroupN = writable<number>(1);

// Read-only derived view of the currently-selected program — plus an
// `update(patch)` shortcut that writes back through `programs`.
function makeSelectedProgram() {
  const inner = derived(
    [programs, selectedSlot],
    ([$programs, $selectedSlot]) =>
      $programs.find((p) => p.slot === $selectedSlot) ?? $programs[0]
  );
  return {
    subscribe: inner.subscribe,
    update(patch: Partial<Program>) {
      const slot = get(selectedSlot);
      programs.update((list) =>
        list.map((p) => (p.slot === slot ? { ...p, ...patch } : p))
      );
      // If the user edited the slot itself, the selection must follow
      // — otherwise the next .update() looks for the old slot, finds
      // nothing, and the rest of the form silently no-ops.
      const targetSlot = patch.slot ?? slot;
      if (patch.slot !== undefined && patch.slot !== slot) {
        selectedSlot.set(patch.slot);
      }
      // Schedule a live-sync write-back. Lazy-imported to avoid a
      // circular dependency (livesync imports programs).
      void import('./livesync').then((m) => m.scheduleProgramWriteback(targetSlot));
    },
  };
}

export const selectedProgram = makeSelectedProgram();

// Derived view of the currently-selected keygroup, with an
// `update(patch)` that writes back through `programs`. Patches can
// target nested fields (soft, loud, mod) via partial helpers.
function makeSelectedKeygroup() {
  const inner = derived(
    [programs, selectedSlot, selectedKeygroupN],
    ([$programs, $selectedSlot, $n]) => {
      const p = $programs.find((p) => p.slot === $selectedSlot) ?? $programs[0];
      return p.keygroups.find((k) => k.n === $n) ?? p.keygroups[0];
    }
  );
  return {
    subscribe: inner.subscribe,
    update(patch: Partial<Keygroup>) {
      const slot = get(selectedSlot);
      const n = get(selectedKeygroupN);
      programs.update((list) =>
        list.map((p) => {
          if (p.slot !== slot) return p;
          return {
            ...p,
            keygroups: p.keygroups.map((k) => (k.n === n ? { ...k, ...patch } : k)),
          };
        })
      );
      // Live-sync the whole program — keygroup edits live inside a
      // PRGM block, no granular per-keygroup write on the S950.
      void import('./livesync').then((m) => m.scheduleProgramWriteback(slot));
    },
    updateSoft(patch: Partial<Layer>) {
      const k = get(inner);
      this.update({ soft: { ...k.soft, ...patch } });
    },
    updateLoud(patch: Partial<Layer>) {
      const k = get(inner);
      this.update({ loud: { ...k.loud, ...patch } });
    },
    updateMod(patch: Partial<Modulation>) {
      const k = get(inner);
      this.update({ mod: { ...k.mod, ...patch } });
    },
  };
}

export const selectedKeygroup = makeSelectedKeygroup();
