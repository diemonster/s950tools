// Program bank + cross-tab "selected program" store.
//
// Like the samples store, the list starts empty: nothing in the app
// is fictional. Entries enter via catalog.refreshCatalog() on Connect,
// or via the (future) "New program" action that allocates a free
// slot for a local-only draft.

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

// Akai's S950 firmware initializes unused sample slots in newly
// created keygroups (and the factory TONE program) with these literal
// placeholder names — they are NOT actual samples on the device.
// Treat them as "unassigned" at the display layer, but keep them in
// `Layer.sample` so round-trip encoding back to the device is exact:
// if the user doesn't change the layer, we re-emit the same bytes.
const PLACEHOLDER_SAMPLE_NAMES = new Set<string>(['2 SAMPLE']);
export function isPlaceholderSample(name: string): boolean {
  return PLACEHOLDER_SAMPLE_NAMES.has(name);
}
// True when the layer references a real sample (user-assigned or
// catalog-matched). Use this to drive UI presence/empty states.
export function isAssignedSample(name: string): boolean {
  return !!name && !isPlaceholderSample(name);
}

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
// new-keygroup defaults so a brand-new program has sane envelope
// shapes instead of all-zero ADSR (would render silent on the device).
export function defaultMod(): Modulation {
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

// Default keygroup — one zone at C2 covering the whole MIDI range,
// no sample bound. Used by the (future) "New keygroup" action and as
// a sentinel inside the empty-state derived stores.
export function newKeygroup(n: number, color = '--rb-yellow'): Keygroup {
  return {
    n, color,
    lowKey: 0, highKey: 127, vel: 128,
    soft: { sample: '', transpose: 0, filter: 99, loudness: 0 },
    loud: { sample: '', transpose: 0, filter: 99, loudness: 0 },
    midiChannel: 16, // OMNI
    voiceOut: 'ALL',
    oneShot: true,
    constPitch: false,
    keyFilter: false,
    mod: defaultMod(),
  };
}

// Skeleton for "New program" — a single keygroup, sensible defaults.
// Mirrors `cli program-template` so the same wire-level shape can be
// reasoned about from either path.
export function newLocalProgram(slot: number, name = ''): Program {
  return {
    slot, name,
    midiProg: 1,
    respondPC: false,
    keyTilt: 0,
    positionalXfade: false,
    keygroups: [newKeygroup(1)],
  };
}

// Lowest unused program slot in `list`, or -1 if all 100 are full.
// Same shape as samples.pickFreeSlot — used by "New program" so a
// fresh draft gets a predictable destination.
export function pickFreeProgramSlot(list: Program[]): number {
  const taken = new Set(list.map((p) => p.slot));
  for (let i = 0; i < 100; i++) {
    if (!taken.has(i)) return i;
  }
  return -1;
}

export const programs = writable<Program[]>([]);
// Default to slot 0 so the sidebar selection survives an empty start.
export const selectedSlot = writable<number>(0);

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
//
// Returns undefined when no programs exist — the component is
// expected to guard via $hasProgram before reading fields.
function makeSelectedKeygroup() {
  const inner = derived(
    [programs, selectedSlot, selectedKeygroupN],
    ([$programs, $selectedSlot, $n]) => {
      const p = $programs.find((p) => p.slot === $selectedSlot) ?? $programs[0];
      if (!p) return undefined;
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
      if (!k) return;
      this.update({ soft: { ...k.soft, ...patch } });
    },
    updateLoud(patch: Partial<Layer>) {
      const k = get(inner);
      if (!k) return;
      this.update({ loud: { ...k.loud, ...patch } });
    },
    updateMod(patch: Partial<Modulation>) {
      const k = get(inner);
      if (!k) return;
      this.update({ mod: { ...k.mod, ...patch } });
    },
  };
}

export const selectedKeygroup = makeSelectedKeygroup();
