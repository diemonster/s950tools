// Catalog refresh — bridges Wails App.Catalog() into the existing
// per-tab stores. On Connect we replace the stubbed programs/samples
// with "skinny" entries: slot + name only, every other field at
// defaults. Selecting an entry triggers a full fetch
// (App.GetProgram / App.GetSampleParams) to populate the rest — see
// ensureProgramLoaded / ensureSampleLoaded below.

import { get } from 'svelte/store';
import * as App from '../../../wailsjs/go/main/App';
import { samples, selectedSampleSlot, type Sample } from './samples';
import {
  programs, selectedSlot,
  type Program, type Keygroup, type Layer, type Modulation,
} from './programs';
import { scanMemory } from './memory';
import { sampleParamsToSample, sampleToSampleParams } from './converters';

// Set of slots whose full data has been fetched from the device. We
// don't refetch on re-selection unless the user explicitly forces it
// (e.g. via a future "Refresh" button).
const loadedPrograms = new Set<number>();
const loadedSamples  = new Set<number>();

// Wire selection changes to lazy-fetch. Subscribed once at module
// load — fires whenever the user clicks a different sidebar entry,
// and once on first import with the initial value (which is a no-op
// because nothing's in the loaded sets yet AND the device isn't
// connected). After Connect+refreshCatalog the loaded sets are
// cleared, so the first click on each new slot fetches.
selectedSlot.subscribe((slot) => {
  // Defer so the rest of the module has finished initialising —
  // ensureProgramLoaded calls App.GetProgram which can resolve
  // asynchronously without interfering with anything.
  void ensureProgramLoaded(slot);
});
selectedSampleSlot.subscribe((slot) => {
  void ensureSampleLoaded(slot);
});

// Palette used to colour keygroup zones when we have no other signal.
// Same nine colours used in the static mockups; cycles by index.
const ZONE_COLORS = [
  '--rb-yellow', '--rb-magenta', '--rb-cyan', '--rb-green',
  '--rb-orange', '--rb-red', '--rb-purple', '--rb-blue', '--rb-pink',
];

// Default sample shape used until SPRM is fetched. `length` is 1
// instead of 0 so the waveform's percentage math doesn't divide by
// zero; the waveform will render flat for an unloaded entry. The
// source is 'device' because skinnySample is only produced from a
// catalog entry — the row exists on the S950.
function skinnySample(slot: number, name: string): Sample {
  return {
    slot, name,
    rate: 26040,
    length: 1,
    start: 0, end: 1,
    loopStart: 0, loopLength: 0,
    mode: 'one-shot',
    reverse: false, velXfade: false,
    tune: 0, loudness: 0,
    source: 'device',
    originalSource: 'device',
  };
}

// Default program shape — no keygroups until the user selects the
// entry and a full GetProgram fetch lands.
function skinnyProgram(slot: number, name: string): Program {
  return {
    slot, name,
    midiProg: 1, respondPC: true,
    keyTilt: 0, positionalXfade: false,
    keygroups: [],
  };
}

// refreshCatalog reads the device's program + sample catalog and
// replaces the local stores. Safe to call multiple times; selections
// are migrated to the new entries if their current slot is gone.
// Clears the "loaded" sets so lazy-fetch will re-run after re-Connect.
export async function refreshCatalog(): Promise<void> {
  const cat = await App.Catalog();

  const newSamples  = (cat.samples  ?? []).map((it) => skinnySample(it.slot, it.name));
  const newPrograms = (cat.programs ?? []).map((it) => skinnyProgram(it.slot, it.name));

  loadedPrograms.clear();
  loadedSamples.clear();

  if (newSamples.length > 0) {
    // Preserve local-only imports through a Connect — they're audio
    // the user dragged in but hasn't uploaded yet, and Connect
    // shouldn't silently wipe that work. If a device slot happens to
    // shadow a local slot, the device wins (it's the source of
    // truth); the local entry is dropped.
    const locals = get(samples).filter((s) => s.source === 'local');
    const deviceSlots = new Set(newSamples.map((s) => s.slot));
    const survivingLocals = locals.filter((s) => !deviceSlots.has(s.slot));
    const merged = [...newSamples, ...survivingLocals].sort((a, b) => a.slot - b.slot);
    samples.set(merged);
    // Keep current selection if its slot still exists; otherwise
    // fall back to the lowest slot so the editor doesn't end up
    // dereffing an empty array.
    const curSlot = get(selectedSampleSlot);
    if (!merged.find((s) => s.slot === curSlot)) {
      selectedSampleSlot.set(merged[0].slot);
    }
  }
  if (newPrograms.length > 0) {
    programs.set(newPrograms);
    const curSlot = get(selectedSlot);
    if (!newPrograms.find((p) => p.slot === curSlot)) {
      selectedSlot.set(newPrograms[0].slot);
    }
  }
}

// ---------- Lazy fetch ----------

// ensureProgramLoaded fetches GetProgram once per slot per connection
// and patches the resulting data into the programs store. Cheap to
// call repeatedly — second call for the same slot is a no-op unless
// `force` is set (e.g. the user explicitly hit "Get from S950" to
// re-pull the latest after a front-panel edit).
export async function ensureProgramLoaded(slot: number, force = false): Promise<void> {
  if (!force && loadedPrograms.has(slot)) return;
  try {
    const json = await App.GetProgram(slot);
    const full = programJSONToProgram(json, slot);
    programs.update((list) => list.map((p) => (p.slot === slot ? full : p)));
    loadedPrograms.add(slot);
    // A user-initiated Get is the right moment to refresh the
    // memory chip: the user may have manually edited the device
    // since Connect (deleted samples on the front panel, etc.),
    // and the chip should reflect that without forcing a reconnect.
    if (force) void scanMemory();
  } catch (e) {
    console.error(`GetProgram(${slot}) failed:`, e);
    throw e;
  }
}

export async function ensureSampleLoaded(slot: number, force = false): Promise<void> {
  if (!force && loadedSamples.has(slot)) return;
  // Skip when the slot has no sample row at all — the merge below
  // is a map-and-filter that would leave the store unchanged
  // anyway, so the SysEx round-trip is pure waste. Also avoids a
  // spurious GetSampleParams at module load (the selectedSampleSlot
  // subscriber auto-fires for the initial value 0 before any
  // catalog refresh has populated the store).
  const existing = get(samples).find((s) => s.slot === slot);
  if (!existing) return;
  // Skip local-only samples — fetching SPRM would either fail (no
  // audio on device for this slot) or, worse, overwrite the
  // imported PCM with phantom device data. Local samples live in
  // host memory only until the user explicitly uploads.
  if (existing.source === 'local') return;
  try {
    const params = await App.GetSampleParams(slot);
    const full = sampleParamsToSample(params, slot);
    // Merge rather than replace so host-side audio attached via
    // import / post-Apply capture survives a lazy SPRM fetch. See
    // memory.ts scanMemory for the same pattern + rationale.
    // originalSource is the immutable origin marker; preserve it
    // explicitly since `full` (from sampleParamsToSample) doesn't
    // know whether this row was loaded fresh from the catalog or
    // already existed as a local import shadowed by a device slot.
    samples.update((list) => list.map((s) =>
      s.slot === slot
        ? { ...full, pcm: s.pcm, words12: s.words12, originalSource: s.originalSource ?? full.originalSource }
        : s,
    ));
    loadedSamples.add(slot);
  } catch (e) {
    console.error(`GetSampleParams(${slot}) failed:`, e);
    throw e;
  }
}

// revertSampleToDevice discards local edits on a sample that was
// originally pulled from the S950. Two paths depending on whether
// we have a pre-edit snapshot:
//
//   A. Snapshot present (the common case after live-edit + resample):
//      restore the snapshot byte-identically (PCM + SPRM fields all
//      come back), AND push the snapshot's SPRM to the device. The
//      push is critical — live-sync may have written intermediate
//      edits to the device's SPRM that no longer match its stored
//      SDATA (e.g. a resample would have changed the SPRM rate
//      without re-uploading audio, causing pitched-down playback).
//      Re-writing the snapshot's SPRM puts the device back in sync
//      with its actual audio bytes.
//
//   B. No snapshot (sample never edited since load): fetch fresh
//      SPRM from the device. Cheap and correct since the device
//      state IS the truth in this case.
//
// originalSource is preserved through both paths so a future
// re-edit can still be reverted.
//
// Throws if the wire call fails. Samples store is untouched on
// failure so local edits survive a transient link blip.
export async function revertSampleToDevice(slot: number): Promise<void> {
  const cur = get(samples).find((s) => s.slot === slot);
  const snap = cur?.deviceSnapshot;
  if (snap) {
    // Path A: restore snapshot, then re-establish device coherence
    // by re-writing the snapshot's SPRM. The device's SDATA was
    // never touched by our live-sync (we only push SPRM), so
    // matching the SPRM back to the snapshot is enough.
    samples.update((xs) => xs.map((x) =>
      x.slot === slot ? { ...snap, deviceSnapshot: undefined } : x,
    ));
    try {
      await App.SetSampleParams(slot, sampleToSampleParams(snap as Sample) as any);
    } catch (e) {
      // Non-fatal: the local state is restored either way. The
      // device's SPRM may still hold the latest edited values
      // until the next manual Send or revert succeeds.
      console.warn(`Revert: SPRM re-sync to device failed:`, e);
    }
    loadedSamples.add(slot);
    return;
  }
  // Path B: no snapshot — fall back to fresh fetch.
  const params = await App.GetSampleParams(slot);
  const fresh = sampleParamsToSample(params, slot);
  samples.update((xs) => xs.map((x) =>
    x.slot === slot
      ? { ...fresh, originalSource: x.originalSource ?? fresh.originalSource }
      : x,
  ));
  loadedSamples.add(slot);
}

// ---------- Converters: protocol → frontend ----------

// programJSONToProgram maps the wire-format JSON from
// protocol.ProgramJSON into the frontend's Program type, with its
// nested layers + modulation block. Fields that have no protocol
// counterpart (constPitch, keyFilter, voiceOut labels) default to
// safe values — they'll write back as no-ops until we model them.
// Exported so the Program tab's Open .json flow can convert a
// loaded JSON without re-implementing the field mapping.
export function programJSONToProgram(j: any, slot: number): Program {
  const keygroups: Keygroup[] = (j.keygroups ?? []).map((k: any, i: number) => ({
    n: i + 1,
    lowKey: k.lower_key ?? 36,
    highKey: k.upper_key ?? 36,
    vel: k.velocity_switch ?? 128,
    soft: layerFrom(k.soft_sample, k.soft_tune, k.soft_filter, k.soft_loudness),
    loud: layerFrom(k.loud_sample, k.loud_tune, k.loud_filter, k.loud_loudness),
    color: ZONE_COLORS[i % ZONE_COLORS.length],
    midiChannel: k.midi_offset ?? 16,
    voiceOut: voiceOutLabel(k.voice_out_assign ?? 255),
    oneShot:    Boolean((k.control_bits ?? 4) & 0x08),
    constPitch: false, // no documented protocol field
    keyFilter:  false, // no documented protocol field
    mod: modulationFrom(k),
    // Stash the 140-byte wire payload for round-trip preservation
    // when we Send back to the device.
    rawBytesHex: k._raw_bytes_hex ?? '',
  }));
  return {
    slot,
    name: (j.name ?? '').trim(),
    midiProg: j.midi_program_number ?? 1,
    respondPC: Boolean(j.enable_midi_program),
    keyTilt: j.key_tilt ?? 0,
    positionalXfade: Boolean(j.positional_xfade),
    keygroups,
    rawHeaderHex: j._raw_header_hex ?? '',
  };
}

function layerFrom(sample: string, tune: number, filter: number, loudness: number): Layer {
  return {
    sample: sample ?? '',
    transpose: tune ?? 0,
    filter: filter ?? 99,
    loudness: loudness ?? 0,
  };
}

function modulationFrom(k: any): Modulation {
  return {
    ampEnv: {
      a: k.attack  ?? 0,  d: k.decay   ?? 80,
      s: k.sustain ?? 99, r: k.release ?? 30,
    },
    filterEnv: {
      a: k.filter_attack  ?? 20, d: k.filter_decay   ?? 20,
      s: k.filter_sustain ?? 20, r: k.filter_release ?? 20,
    },
    envToVCF: k.adsr_to_vcf ?? 0,
    keyTrack: k.filter_key_tracking ?? 50,
    lfo: {
      rate:       k.lfo_rate              ?? 42,
      depth:      k.lfo_depth             ?? 0,
      fadeIn:     k.lfo_build_time        ?? 64,
      modWheel:   k.mod_wheel_lfo_depth_mod ?? 50,
      aftertouch: k.aftertouch_depth_mod  ?? 0,
    },
    vel: {
      toFilter:  k.filter_vel_int   ?? 10,
      toLoud:    k.loudness_vel_int ?? 30,
      toAttack:  k.attack_vel_int   ?? 0,
      toRelease: k.vel_release_int  ?? 0,
    },
    warpAmount: k.pitch_warp_vel_int ?? 0,
  };
}

function voiceOutLabel(n: number): string {
  if (n === 255) return 'ALL';
  if (n === 8)   return 'L';
  if (n === 9)   return 'R';
  if (n >= 0 && n <= 7) return String(n + 1);
  return 'ALL';
}

// sampleParamsToSample now lives in ./converters so memory.ts can
// share the SPRM → Sample mapping without forming an import cycle.
