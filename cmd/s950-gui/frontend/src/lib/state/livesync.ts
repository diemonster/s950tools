// Live-sync — debounces SetProgram / SetSampleParams calls so user
// edits propagate to the device without re-uploading on every
// keystroke. Triggered explicitly from store updaters; loads (catalog
// refresh, Get from S950) bypass this path so we don't immediately
// re-Send what we just read.
//
// Sync chip states emitted via setSync():
//   sending → request in flight
//   synced  → last write succeeded
//   error   → last write failed (chip stays red until the next edit)

import { get } from 'svelte/store';
import * as App from '../../../wailsjs/go/main/App';
import { programs, type Program, type Keygroup } from './programs';
import { samples, type Sample } from './samples';
import { phase } from './connection';
import { setSync } from '../sync';
import { sampleToSampleParams } from './converters';

// Debounce window. 400ms is the sweet spot per design discussion:
// long enough that a knob sweep settles into one Send, short enough
// the device update feels live during slow edits.
const DEBOUNCE_MS = 400;

let programTimer: ReturnType<typeof setTimeout> | null = null;
let programPendingSlot: number | null = null;

let sampleTimer: ReturnType<typeof setTimeout> | null = null;
let samplePendingSlot: number | null = null;

// Connection guard — pre-flight before scheduling. Trying to Send to
// a disconnected device would NAK; the sync chip already shows the
// link state via Topbar so silent skip is the right behaviour here.
function isConnected(): boolean {
  return get(phase) === 'connected';
}

// ---------- Program live-sync ----------

export function scheduleProgramWriteback(slot: number) {
  if (!isConnected()) return;
  programPendingSlot = slot;
  setSync('dirty', 'Editing...');
  if (programTimer) clearTimeout(programTimer);
  programTimer = setTimeout(flushProgramWriteback, DEBOUNCE_MS);
}

// flushProgramWriteback runs the actual SetProgram for the most
// recently-dirty slot. Exposed so the explicit "Send to S950"
// button can fire an immediate write without waiting for the
// debounce.
export async function flushProgramWriteback() {
  if (programTimer) {
    clearTimeout(programTimer);
    programTimer = null;
  }
  const slot = programPendingSlot;
  if (slot === null) return;
  programPendingSlot = null;

  const p = get(programs).find((x) => x.slot === slot);
  if (!p) return;

  setSync('sending', 'Sending...');
  try {
    await App.SetProgram(slot, programToJSON(p));
    setSync('synced', 'Synced');
  } catch (e: any) {
    console.error(`SetProgram(${slot}) failed:`, e);
    setSync('error', 'Sync error');
  }
}

// ---------- Sample SPRM live-sync ----------

export function scheduleSampleWriteback(slot: number) {
  if (!isConnected()) return;
  // Only live-sync samples that are coherent with the device. Once
  // an edit has flipped source to 'local' (e.g. resample changed
  // the host PCM), pushing SPRM to the device would mismatch its
  // unchanged stored SDATA — SPRM would say "rate 22050" while the
  // device still has the original-rate audio bytes, producing
  // pitched-down playback on the next trigger. The user has to
  // explicitly Send-to-S950 (which uploads both SDATA + SPRM
  // together) before live-sync makes sense again.
  const s = get(samples).find((x) => x.slot === slot);
  if (!s || s.source !== 'device') return;
  samplePendingSlot = slot;
  setSync('dirty', 'Editing...');
  if (sampleTimer) clearTimeout(sampleTimer);
  sampleTimer = setTimeout(flushSampleWriteback, DEBOUNCE_MS);
}

export async function flushSampleWriteback() {
  if (sampleTimer) {
    clearTimeout(sampleTimer);
    sampleTimer = null;
  }
  const slot = samplePendingSlot;
  if (slot === null) return;
  samplePendingSlot = null;

  const s = get(samples).find((x) => x.slot === slot);
  if (!s) return;

  setSync('sending', 'Sending...');
  try {
    await App.SetSampleParams(slot, sampleToSampleParams(s) as any);
    setSync('synced', 'Synced');
  } catch (e: any) {
    console.error(`SetSampleParams(${slot}) failed:`, e);
    setSync('error', 'Sync error');
  }
}

// ---------- Frontend → wire converters ----------

// programToJSON inverts catalog.ts's programJSONToProgram. The
// _raw_*_hex strings are passed through unchanged so undocumented
// bytes round-trip; if a program was never loaded from the device
// (e.g. freshly built by slicing), the raw fields are empty and
// the backend's encoder will use NewDefaultProgram's seed bytes.
export function programToJSON(p: Program): any {
  return {
    name: p.name,
    key_tilt: p.keyTilt,
    positional_xfade: p.positionalXfade,
    num_keygroups: p.keygroups.length,
    midi_program_number: p.midiProg,
    enable_midi_program: p.respondPC,
    keygroups: p.keygroups.map((kg) => keygroupToJSON(kg)),
    _raw_header_hex: p.rawHeaderHex ?? '',
  };
}

function keygroupToJSON(kg: Keygroup): any {
  // Reassemble control_bits from the toggle UI. We expose two bits:
  //   bit 0 (transpose OFF) — driven by the "Const-pitch" toggle
  //   bit 3 (one-shot)      — driven by the "One-shot" toggle
  // The other bits (vibrato-desync, vel-release, vel-xfade variants)
  // aren't surfaced; the default 4 keeps vibrato-desync on. Bits
  // not in our UI are NOT preserved across edits — if a future
  // round-trip needs them, decode rawBytesHex on the Go side and
  // mask in our exposed bits there.
  let ctrl = 4;
  if (kg.oneShot)    ctrl |= 0x08;
  if (kg.constPitch) ctrl |= 0x01;
  return {
    lower_key: kg.lowKey,
    upper_key: kg.highKey,
    velocity_switch: kg.vel,
    attack:  kg.mod.ampEnv.a,
    decay:   kg.mod.ampEnv.d,
    sustain: kg.mod.ampEnv.s,
    release: kg.mod.ampEnv.r,
    filter_attack:  kg.mod.filterEnv.a,
    filter_decay:   kg.mod.filterEnv.d,
    filter_sustain: kg.mod.filterEnv.s,
    filter_release: kg.mod.filterEnv.r,
    filter_vel_int: kg.mod.vel.toFilter,
    filter_key_tracking: kg.mod.keyTrack,
    attack_vel_int:  kg.mod.vel.toAttack,
    vel_release_int: kg.mod.vel.toRelease,
    loudness_vel_int: kg.mod.vel.toLoud,
    pitch_warp_vel_int: kg.mod.warpAmount,
    pitch_warp_offset:  0,
    pitch_warp_recovery: 99,
    adsr_to_vcf: kg.mod.envToVCF,
    aftertouch_depth_mod: kg.mod.lfo.aftertouch,
    mod_wheel_lfo_depth_mod: kg.mod.lfo.modWheel,
    lfo_build_time: kg.mod.lfo.fadeIn,
    lfo_rate:       kg.mod.lfo.rate,
    lfo_depth:      kg.mod.lfo.depth,
    control_bits:   ctrl,
    voice_out_assign: voiceOutByte(kg.voiceOut),
    midi_offset: kg.midiChannel,
    vel_xfade_50pct: 64,
    soft_sample:   kg.soft.sample,
    soft_tune:     kg.soft.transpose,
    soft_filter:   kg.soft.filter,
    soft_loudness: kg.soft.loudness,
    loud_sample:   kg.loud.sample,
    loud_tune:     kg.loud.transpose,
    loud_filter:   kg.loud.filter,
    loud_loudness: kg.loud.loudness,
    _raw_bytes_hex: kg.rawBytesHex ?? '',
  };
}

function voiceOutByte(label: string): number {
  if (label === 'ALL') return 255;
  if (label === 'L')   return 8;
  if (label === 'R')   return 9;
  const n = parseInt(label, 10);
  if (!isNaN(n) && n >= 1 && n <= 8) return n - 1;
  return 255;
}

// sampleToParams used to live here as a parallel mapping. It was
// merged into ./converters.ts (sampleToSampleParams) so the live-
// edit debounce path and the explicit Send-SPRM path can't drift
// — every previous bug in this file (e.g. the inverted tune
// direction) had to be fixed twice. Single source of truth wins.
