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
  } catch (e) {
    console.error(`GetProgram(${slot}) failed:`, e);
    throw e;
  }
}

export async function ensureSampleLoaded(slot: number, force = false): Promise<void> {
  if (!force && loadedSamples.has(slot)) return;
  // Skip local-only samples — fetching SPRM would either fail (no
  // audio on device for this slot) or, worse, overwrite the
  // imported PCM with phantom device data. Local samples live in
  // host memory only until the user explicitly uploads.
  const existing = get(samples).find((s) => s.slot === slot);
  if (existing && existing.source === 'local') return;
  try {
    const params = await App.GetSampleParams(slot);
    const full = sampleParamsToSample(params, slot);
    samples.update((list) => list.map((s) => (s.slot === slot ? full : s)));
    loadedSamples.add(slot);
  } catch (e) {
    console.error(`GetSampleParams(${slot}) failed:`, e);
    throw e;
  }
}

// ---------- Converters: protocol → frontend ----------

// programJSONToProgram maps the wire-format JSON from
// protocol.ProgramJSON into the frontend's Program type, with its
// nested layers + modulation block. Fields that have no protocol
// counterpart (constPitch, keyFilter, voiceOut labels) default to
// safe values — they'll write back as no-ops until we model them.
function programJSONToProgram(j: any, slot: number): Program {
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

// sampleParamsToSample maps protocol.SampleParams (high-level decode
// of the 120-byte SPRM block) into the frontend's Sample type. The
// frontend uses string literals for the replay mode while the wire
// format uses ASCII bytes ('O' / 'L' / 'A').
function sampleParamsToSample(p: any, slot: number): Sample {
  const total = p.TotalWords ?? 0;
  const start = p.Start ?? 0;
  const end   = p.End ?? total;
  return {
    slot,
    name: (p.Name ?? '').trim(),
    rate: p.SampleRateHz ?? 26040,
    length: total || 1,
    start,
    end,
    // S950 stores LoopLength (not loop end); LoopStart isn't a
    // separate field — the device's SPRM uses Start as the loop
    // origin. The frontend renders the loop region as
    // [loopStart..loopStart+loopLength], so we read LoopLength
    // directly and anchor loopStart to Start when present.
    loopStart: start,
    loopLength: p.LoopLength ?? 0,
    mode: replayModeFrom(p.ReplayMode),
    reverse: (p.Reversed ?? 0x4E) === 0x52, // 'R' = reverse
    velXfade: (p.VelXFade ?? 0) === 255,
    tune: ((p.NominalPitch ?? 960) - 960) / 16, // semitones from C3
    loudness: p.LoudOffset ?? 0,
    // Stash the 120-byte SPRM block for round-trip on Send.
    // Wails sends [120]byte as a number array; we mirror that.
    raw: Array.from(p.Raw ?? []),
    // Definitionally 'device' — this converter only runs after a
    // successful GetSampleParams round trip.
    source: 'device',
  } as Sample;
}

function replayModeFrom(b: number | undefined): Sample['mode'] {
  // 'O' = 79 = one-shot, 'L' = 76 = loop, 'A' = 65 = alternating
  // ('alternating loop' in the S950 manual; surfaced as ping-pong
  // throughout the UI so sample-level + slice-level terminology lines up).
  switch (b) {
    case 76: return 'loop';
    case 65: return 'ping-pong';
    default: return 'one-shot';
  }
}
