import { describe, it, expect, beforeEach } from 'vitest';
import { get } from 'svelte/store';
import {
  buildEvenSlices,
  buildAutoSlices,
  slicesFor,
  MAX_SLICES,
  commitSlices,
  hasCommittedChildren,
  updateSlicing,
  slicing,
} from './slicing';
import { samples, selectedSampleSlot, newLocalSample, type Sample } from './samples';

// Reset the global stores before each test so they don't leak state
// across cases. samples + selectedSampleSlot are module-level singletons.
beforeEach(() => {
  samples.set([]);
  selectedSampleSlot.set(0);
});

describe('slicesFor', () => {
  it('multiplies bars × division', () => {
    expect(slicesFor(1, 8)).toBe(8);
    expect(slicesFor(2, 16)).toBe(32);
    expect(slicesFor(4, 4)).toBe(16);
  });
  it('caps at MAX_SLICES', () => {
    expect(slicesFor(16, 32)).toBe(MAX_SLICES);
    // bars × division large enough to trip the cap from the other side.
    expect(slicesFor(100, 32)).toBe(MAX_SLICES);
  });
  it('clamps to a minimum of 1', () => {
    expect(slicesFor(0, 8)).toBe(1);
    expect(slicesFor(-5, 8)).toBe(1);
  });
});

describe('buildEvenSlices', () => {
  it('produces N evenly-spaced slices covering the full length', () => {
    const out = buildEvenSlices(4, 1000);
    expect(out).toHaveLength(4);
    expect(out[0].start).toBe(0);
    expect(out[3].start + out[3].length).toBe(1000);
  });
  it('the last slice absorbs the remainder when length is not divisible', () => {
    const out = buildEvenSlices(3, 1001);
    const total = out.reduce((s, sl) => s + sl.length, 0);
    expect(total).toBe(1001);
    // First two are floor(1001/3) = 333; last is 1001-666 = 335.
    expect(out[0].length).toBe(333);
    expect(out[2].length).toBe(335);
  });
  it('returns an empty array for degenerate inputs', () => {
    expect(buildEvenSlices(0, 1000)).toEqual([]);
    expect(buildEvenSlices(4, 0)).toEqual([]);
  });
  it('seeds every slice as one-shot with no loop', () => {
    const [first] = buildEvenSlices(2, 200);
    expect(first.loopMode).toBe('one-shot');
    expect(first.loopStart).toBe(0);
    expect(first.loopLength).toBe(0);
  });
});

describe('buildAutoSlices', () => {
  it('is deterministic for the same seed', () => {
    const a = buildAutoSlices(6, 12000, 0x9E37);
    const b = buildAutoSlices(6, 12000, 0x9E37);
    expect(a).toEqual(b);
  });
  it('keeps slice 0 anchored at sample start', () => {
    const out = buildAutoSlices(8, 8000);
    expect(out[0].start).toBe(0);
  });
  it('keeps slices in non-decreasing order', () => {
    const out = buildAutoSlices(10, 16000);
    for (let i = 1; i < out.length; i++) {
      expect(out[i].start).toBeGreaterThanOrEqual(out[i - 1].start);
    }
  });
  it('still covers the full length (last slice runs to end)', () => {
    const out = buildAutoSlices(5, 5000);
    const last = out[out.length - 1];
    expect(last.start + last.length).toBe(5000);
  });
});

describe('commitSlices', () => {
  // Helper: install one source sample with synthetic audio so the
  // commit path can extract subarrays out of it.
  function setupSource(slot = 0, length = 1000): Sample {
    const pcm     = Array.from({ length }, (_, i) => i);
    const words12 = Array.from({ length }, (_, i) => (i & 0xFFF));
    const src: Sample = {
      ...newLocalSample(slot, 'KICK', 26040, length),
      pcm,
      words12,
    };
    samples.set([src]);
    selectedSampleSlot.set(slot);
    return src;
  }

  it('does nothing when slicing is inactive', () => {
    setupSource();
    updateSlicing({ active: false, slices: buildEvenSlices(4, 1000) });
    const allocated = commitSlices();
    expect(allocated).toEqual([]);
    expect(get(samples)).toHaveLength(1);
  });

  it('refuses when the source has no host-side audio', () => {
    const slot = 5;
    samples.set([newLocalSample(slot, 'EMPTY', 26040, 1000)]); // no pcm/words12
    selectedSampleSlot.set(slot);
    updateSlicing({ active: true, slices: buildEvenSlices(4, 1000) });
    const allocated = commitSlices();
    expect(allocated).toEqual([]);
    // No children were added.
    expect(get(samples)).toHaveLength(1);
  });

  it('creates N local children with extracted PCM + words12', () => {
    setupSource(0, 1000);
    updateSlicing({ active: true, slices: buildEvenSlices(4, 1000) });
    const allocated = commitSlices();
    expect(allocated).toHaveLength(4);
    const list = get(samples);
    expect(list).toHaveLength(5); // 1 source + 4 children

    // Each child carries parentSlot back to the source and source: 'local'.
    const children = list.filter((s) => s.parentSlot === 0);
    expect(children).toHaveLength(4);
    for (const c of children) {
      expect(c.source).toBe('local');
      expect(c.words12?.length).toBeGreaterThan(0);
      expect(c.pcm?.length).toBe(c.words12?.length);
    }
    // Concatenated child audio reconstructs the source.
    const concatWords = children
      .sort((a, b) => a.slot - b.slot)
      .flatMap((c) => c.words12!);
    expect(concatWords).toHaveLength(1000);
  });

  it('skips slots already occupied (auto-picks free slots)', () => {
    setupSource(0, 1000); // slot 0 occupied
    // Pre-occupy slots 1 and 2 too.
    samples.update((xs) => [
      ...xs,
      newLocalSample(1, 'X', 26040, 1),
      newLocalSample(2, 'Y', 26040, 1),
    ]);
    updateSlicing({ active: true, slices: buildEvenSlices(2, 1000) });
    const allocated = commitSlices();
    // Two slices → slots 3 and 4 (the next two free).
    expect(allocated).toEqual([3, 4]);
  });

  it('truncates child names to the S950 10-char limit', () => {
    setupSource(0, 1000);
    samples.update((xs) =>
      xs.map((s) => (s.slot === 0 ? { ...s, name: 'BREAKBEAT' } : s)),
    );
    updateSlicing({ active: true, slices: buildEvenSlices(2, 1000) });
    commitSlices();
    const children = get(samples).filter((s) => s.parentSlot === 0);
    for (const c of children) {
      expect(c.name.length).toBeLessThanOrEqual(10);
    }
  });

  it('deactivates slicing after a successful commit', () => {
    setupSource(0, 1000);
    updateSlicing({ active: true, slices: buildEvenSlices(2, 1000) });
    commitSlices();
    expect(get(slicing).active).toBe(false);
  });

  it('propagates per-slice loop config onto the child sample', () => {
    setupSource(0, 1000);
    const evens = buildEvenSlices(2, 1000);
    evens[1].loopMode = 'ping-pong';
    evens[1].loopStart = 50;
    evens[1].loopLength = 200;
    updateSlicing({ active: true, slices: evens });
    commitSlices();

    const children = get(samples)
      .filter((s) => s.parentSlot === 0)
      .sort((a, b) => a.slot - b.slot);
    expect(children[1].mode).toBe('ping-pong');
    expect(children[1].loopStart).toBe(50);
    expect(children[1].loopLength).toBe(200);
  });
});

describe('hasCommittedChildren', () => {
  it('is false when the current source has no children', () => {
    samples.set([newLocalSample(0, 'A', 26040, 100)]);
    selectedSampleSlot.set(0);
    expect(get(hasCommittedChildren)).toBe(false);
  });

  it('is true while local children point at the current source', () => {
    samples.set([
      newLocalSample(0, 'A', 26040, 100),
      { ...newLocalSample(1, 'A_01', 26040, 50), parentSlot: 0 },
    ]);
    selectedSampleSlot.set(0);
    expect(get(hasCommittedChildren)).toBe(true);
  });

  it('flips back to false once the children become device samples', () => {
    samples.set([
      newLocalSample(0, 'A', 26040, 100),
      { ...newLocalSample(1, 'A_01', 26040, 50), parentSlot: 0, source: 'device' },
    ]);
    selectedSampleSlot.set(0);
    expect(get(hasCommittedChildren)).toBe(false);
  });

  it('is per-source: switching to a different selected slot resets the lock', () => {
    samples.set([
      newLocalSample(0, 'A', 26040, 100),
      newLocalSample(7, 'B', 26040, 100),
      { ...newLocalSample(1, 'A_01', 26040, 50), parentSlot: 0 },
    ]);
    selectedSampleSlot.set(7); // viewing 'B', which has no children
    expect(get(hasCommittedChildren)).toBe(false);
    selectedSampleSlot.set(0); // back to 'A' — locked again
    expect(get(hasCommittedChildren)).toBe(true);
  });
});
