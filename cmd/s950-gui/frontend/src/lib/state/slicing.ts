// Slicing state — per-sample. Each S950 sample slot can be sliced
// independently; the state map is keyed by slot so swapping samples
// preserves whatever slicing the user set up on each.
//
// Slicing is a desktop pre-processing step: when the user clicks
// "Apply", the host code uploads each slice as a separate sample to
// consecutive free slots and auto-generates a program that maps the
// slices chromatically (C2 = slice 1, C#2 = slice 2, …). The S950
// itself can't slice on-device.

import { writable, derived, get } from 'svelte/store';
import { samples, selectedSampleSlot, selectedSample, newLocalSample, type Sample } from './samples';

export type SliceMode = 'manual' | 'auto' | 'beats';
export type SliceLoopMode = 'one-shot' | 'loop' | 'ping-pong';
export type Division = 4 | 8 | 16 | 32;

export type Slice = {
  start: number;       // word index in the source sample
  length: number;      // words
  loopMode: SliceLoopMode;
  loopStart: number;   // relative to slice start
  loopLength: number;
};

export type SlicingState = {
  active: boolean;
  mode: SliceMode;
  tempo: number;        // BPM — informational for Beats mode
  bars: number;
  division: Division;
  snapToZero: boolean;
  slices: Slice[];
  selectedIndex: number;
  zoom: number;         // 1.0 = 100% (fits the full sample). 2.0 = 2× in.
  zoomCenter: number;   // 0..1, the word-position the zoom anchors on
};

export const MAX_SLICES = 64;

// How many evenly-spaced slices fit in `bars` × `division`. 4/4 time.
// 1/4 = 4 per bar, 1/8 = 8 per bar, 1/16 = 16 per bar, 1/32 = 32 per bar.
export function slicesFor(bars: number, division: Division): number {
  return Math.min(MAX_SLICES, Math.max(1, bars * division));
}

function defaultState(): SlicingState {
  return {
    active: false,
    mode: 'beats',
    tempo: 120,
    bars: 1,
    division: 8,
    snapToZero: true,
    slices: [],
    selectedIndex: 0,
    zoom: 1,
    zoomCenter: 0.5,
  };
}

// Build N evenly-spaced slices that span [0..sampleLength].
export function buildEvenSlices(n: number, sampleLength: number): Slice[] {
  if (n <= 0 || sampleLength <= 0) return [];
  const each = Math.floor(sampleLength / n);
  const out: Slice[] = [];
  let cursor = 0;
  for (let i = 0; i < n; i++) {
    const last = i === n - 1;
    const length = last ? sampleLength - cursor : each;
    out.push({
      start: cursor,
      length,
      loopMode: 'one-shot',
      loopStart: 0,
      loopLength: 0,
    });
    cursor += each;
  }
  return out;
}

// Build a slice array that looks auto-detected — same shape as
// buildEvenSlices but with mild jitter so it doesn't look perfectly
// regular. Used as a placeholder for real transient detection.
export function buildAutoSlices(n: number, sampleLength: number, seed = 0x9E37): Slice[] {
  let s = seed >>> 0;
  const rnd = () => {
    s = (s * 1103515245 + 12345) & 0x7fffffff;
    return s / 0x7fffffff;
  };
  const even = buildEvenSlices(n, sampleLength);
  const slack = Math.floor(sampleLength / n * 0.25);
  const out: Slice[] = [];
  for (let i = 0; i < even.length; i++) {
    const jitter = i === 0 ? 0 : Math.floor((rnd() - 0.5) * 2 * slack);
    const nominal = even[i].start;
    const start = Math.max(0, Math.min(sampleLength - 1, nominal + jitter));
    out.push({ ...even[i], start });
  }
  // Recompute lengths from adjacent starts.
  for (let i = 0; i < out.length; i++) {
    const next = i < out.length - 1 ? out[i + 1].start : sampleLength;
    out[i].length = Math.max(1, next - out[i].start);
  }
  return out;
}

// ---------- Store ----------

const stateBySlot = writable<Record<number, SlicingState>>({});

export const slicing = derived(
  [stateBySlot, selectedSampleSlot],
  ([$state, $slot]) => $state[$slot] ?? defaultState()
);

export function updateSlicing(patch: Partial<SlicingState>) {
  const slot = get(selectedSampleSlot);
  stateBySlot.update((m) => ({
    ...m,
    [slot]: { ...defaultState(), ...m[slot], ...patch },
  }));
}

// Drop any persisted slicing config for these slots. Called after a
// successful Apply: the source slot and every destination slot have
// just been overwritten by NEW device samples (the slice children),
// so the parent's slice marks would otherwise appear on top of the
// freshly-uploaded child waveforms. Keeps state for unrelated slots
// the user might be editing in parallel.
export function clearSlicingForSlots(slots: number[]) {
  if (slots.length === 0) return;
  stateBySlot.update((m) => {
    const next = { ...m };
    for (const s of slots) delete next[s];
    return next;
  });
}

// Convenience helpers for nested updates.
export function setSliceMode(mode: SliceMode) {
  const cur = get(slicing);
  const length = get(selectedSample).length;
  const next: Partial<SlicingState> = { mode };
  if (mode === 'beats') {
    next.slices = buildEvenSlices(slicesFor(cur.bars, cur.division), length);
  } else if (mode === 'auto') {
    // Real impl runs transient detection. For now, placeholder evens
    // with jitter — same surface area as future call site.
    next.slices = buildAutoSlices(8, length);
  } else if (mode === 'manual') {
    // Keep whatever's there. If empty, seed with one slice covering
    // the whole sample so the user has something to drag.
    if (cur.slices.length === 0) {
      next.slices = [{
        start: 0, length, loopMode: 'one-shot', loopStart: 0, loopLength: 0,
      }];
    }
  }
  next.selectedIndex = 0;
  updateSlicing(next);
}

export function setBeats(patch: Partial<Pick<SlicingState, 'tempo' | 'bars' | 'division'>>) {
  const cur = get(slicing);
  const length = get(selectedSample).length;
  const merged = { ...cur, ...patch };
  updateSlicing({
    ...patch,
    slices: buildEvenSlices(slicesFor(merged.bars, merged.division), length),
    selectedIndex: 0,
  });
}

export function selectSlice(idx: number) {
  updateSlicing({ selectedIndex: idx });
}

export function updateSliceAt(idx: number, patch: Partial<Slice>) {
  const cur = get(slicing);
  const slices = cur.slices.map((s, i) => i === idx ? { ...s, ...patch } : s);
  updateSlicing({ slices });
}

export function addSliceAt(wordIndex: number) {
  const cur = get(slicing);
  const length = get(selectedSample).length;
  // Insert in sorted order, then recompute lengths so adjacents fit.
  const sorted = [...cur.slices.map((s) => s.start), wordIndex].sort((a, b) => a - b);
  const next: Slice[] = sorted.map((start, i) => {
    const end = i < sorted.length - 1 ? sorted[i + 1] : length;
    return {
      start,
      length: Math.max(1, end - start),
      loopMode: 'one-shot',
      loopStart: 0,
      loopLength: 0,
    };
  });
  updateSlicing({
    slices: next,
    selectedIndex: sorted.indexOf(wordIndex),
  });
}

// ---------- Commit ----------
//
// Materialise the current slicing config as N local Sample rows in
// the sidebar. The source sample's audio is sliced in-place — we
// take subarrays of pcm + words12 for each slice and stamp them on
// the new sample. Each child carries a parentSlot pointing back to
// the source so the UI can lock re-commit until the children have
// been uploaded (source: 'device').
//
// Commit + Apply Slicing are *alternative* endpoints from the same
// state: commit-then-apply uses the committed children as the upload
// payload, so we never end up with both local children AND new
// device samples for the same slicing run.
//
// Returns the new slot numbers (or [] on failure — e.g. when the
// source has no host-side audio or the device is full).
export function commitSlices(): number[] {
  const cur = get(slicing);
  if (!cur.active || cur.slices.length === 0) return [];
  const src = get(selectedSample);
  if (!src || !src.words12 || src.words12.length === 0) {
    console.warn('commitSlices: source has no host-side audio (import first).');
    return [];
  }

  // Allocate slots up front so a partial commit can't half-fill the
  // sidebar. Take into account both existing rows and slots already
  // reserved earlier in this loop.
  const taken = new Set(get(samples).map((s) => s.slot));
  const allocated: number[] = [];
  for (let i = 0; i < cur.slices.length; i++) {
    let slot = -1;
    for (let n = 0; n < 100; n++) {
      if (!taken.has(n)) { slot = n; taken.add(n); break; }
    }
    if (slot < 0) {
      console.warn('commitSlices: out of free sample slots');
      return [];
    }
    allocated.push(slot);
  }

  const baseName = (src.name || 'SLICE').toUpperCase();
  const children: Sample[] = cur.slices.map((sl, i) => {
    const start = Math.max(0, sl.start | 0);
    const end   = Math.min(src.words12!.length, (sl.start + sl.length) | 0);
    const wordsSlice = src.words12!.slice(start, end);
    const pcmSlice   = src.pcm ? src.pcm.slice(start, end) : undefined;
    // S950 names are capped at 10 ASCII chars. "{base}_NN" keeps the
    // ordering visible in the sidebar even when the base is short.
    const sliceName = `${baseName}_${String(i + 1).padStart(2, '0')}`.slice(0, 10);
    return {
      ...newLocalSample(allocated[i], sliceName, src.rate, end - start),
      mode: sl.loopMode,
      loopStart: sl.loopStart,
      loopLength: sl.loopLength,
      pcm: pcmSlice,
      words12: wordsSlice,
      parentSlot: src.slot,
    };
  });

  // Single atomic update so subscribers see all children at once.
  samples.update((xs) => [...xs, ...children].sort((a, b) => a.slot - b.slot));
  // Deactivate slicing on the source — slices are now "materialised";
  // re-entering slicing mode would let the user start over but is
  // gated by the Commit lock until the children are uploaded.
  // Selection stays on the source: the Apply button lives on that
  // row, and keeping context lets the user immediately upload once
  // they're done eyeballing the children in the sidebar.
  updateSlicing({ active: false });
  return allocated;
}

// Reactive lock: true while the current source has any local
// (un-uploaded) committed children. The Commit button binds to this.
// Goes false again once those children flip to source: 'device'
// after an Apply Slicing run.
export const hasCommittedChildren = derived(
  [samples, selectedSampleSlot],
  ([$samples, $slot]) =>
    $samples.some((s) => s.parentSlot === $slot && s.source === 'local'),
);

export function removeSliceAt(idx: number) {
  const cur = get(slicing);
  if (cur.slices.length <= 1) return; // keep at least one slice
  const length = get(selectedSample).length;
  const next: Slice[] = cur.slices.filter((_, i) => i !== idx);
  // Recompute lengths after removal so neighbors expand into the gap.
  for (let i = 0; i < next.length; i++) {
    const end = i < next.length - 1 ? next[i + 1].start : length;
    next[i].length = Math.max(1, end - next[i].start);
  }
  updateSlicing({
    slices: next,
    selectedIndex: Math.min(idx, next.length - 1),
  });
}

// ---------- Host audio capture across Apply ----------
//
// After ApplySlicing succeeds, refreshCatalog repopulates the
// samples store with fresh device-sourced rows that have no host
// PCM/words12 — so the waveform display falls back to the synthetic
// squiggle even though the host held the real audio milliseconds
// earlier. Phase 1A keeps that audio alive in memory across the
// Apply → refreshCatalog transition: capture per-slice subarrays
// BEFORE the wire round trip, then re-attach to the matching device
// slots AFTER refreshCatalog lands. Pure functions so the round-trip
// is testable without mocking the Wails bridge.

export type CapturedAudio = { pcm?: number[]; words12: number[] };

// Slice the parent's host audio into per-destination-slot chunks.
// Each slice spec contributes one entry, keyed by the slot it will
// land on (the device picks slots; we pass through what
// InspectSlicing reported in `sampleSlots`). Slices past the source's
// tail are clamped — we'd rather return a short capture than throw
// during a successful upload.
export function captureSliceAudio(
  source: { words12?: number[]; pcm?: number[] },
  slices: ReadonlyArray<{ start: number; length: number }>,
  destSlots: ReadonlyArray<number>,
): Map<number, CapturedAudio> {
  const out = new Map<number, CapturedAudio>();
  const w = source.words12 ?? [];
  const p = source.pcm;
  const n = Math.min(slices.length, destSlots.length);
  for (let i = 0; i < n; i++) {
    const sl = slices[i];
    const start = Math.max(0, sl.start);
    const end = Math.min(w.length, start + Math.max(0, sl.length));
    out.set(destSlots[i], {
      words12: w.slice(start, end),
      pcm: p ? p.slice(start, end) : undefined,
    });
  }
  return out;
}

// Walk a samples array and overlay any captured audio whose slot
// matches. Returns a new array (does not mutate); rows without a
// match in the capture map are passed through untouched. Used after
// refreshCatalog to restore host audio to the freshly-loaded device
// rows.
export function attachAudioToSamples(
  list: ReadonlyArray<Sample>,
  audioBySlot: ReadonlyMap<number, CapturedAudio>,
): Sample[] {
  if (audioBySlot.size === 0) return list.slice();
  return list.map((s) => {
    const cap = audioBySlot.get(s.slot);
    if (!cap) return s;
    return { ...s, pcm: cap.pcm, words12: cap.words12 };
  });
}
