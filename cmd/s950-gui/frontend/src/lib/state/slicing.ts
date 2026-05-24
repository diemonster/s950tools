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
import { selectedSampleSlot, selectedSample } from './samples';

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

// Convenience helpers for nested updates.
export function setSliceMode(mode: SliceMode) {
  const cur = get(slicing);
  const length = get(selectedSample).length;
  let next: Partial<SlicingState> = { mode };
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
