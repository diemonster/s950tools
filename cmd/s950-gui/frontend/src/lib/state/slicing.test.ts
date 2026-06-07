import { describe, it, expect, beforeEach } from 'vitest';
import { get } from 'svelte/store';
import {
  buildEvenSlices,
  buildAutoSlices,
  buildBeatSlices,
  slicesFor,
  expectedSeconds,
  findZeroCrossing,
  MAX_SLICES,
  commitSlices,
  hasCommittedChildren,
  updateSlicing,
  slicing,
  captureSliceAudio,
  attachAudioToSamples,
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

describe('captureSliceAudio', () => {
  // Synthetic parent: 100 words of monotonically increasing values so
  // every slice's subarray is trivially verifiable (start..end maps
  // directly to value range).
  const SOURCE = {
    words12: Array.from({ length: 100 }, (_, i) => i),
    pcm: Array.from({ length: 100 }, (_, i) => i / 100),
  };

  it('returns a slot-keyed map of word + pcm subarrays', () => {
    const map = captureSliceAudio(
      SOURCE,
      [{ start: 0, length: 40 }, { start: 40, length: 60 }],
      [5, 7],
    );
    expect(map.size).toBe(2);
    expect(map.get(5)?.words12).toEqual(SOURCE.words12.slice(0, 40));
    expect(map.get(5)?.pcm).toEqual(SOURCE.pcm.slice(0, 40));
    expect(map.get(7)?.words12).toEqual(SOURCE.words12.slice(40, 100));
    expect(map.get(7)?.pcm).toEqual(SOURCE.pcm.slice(40, 100));
  });

  it('omits pcm when the source has only words12', () => {
    const map = captureSliceAudio(
      { words12: SOURCE.words12 },
      [{ start: 0, length: 20 }],
      [0],
    );
    expect(map.get(0)?.pcm).toBeUndefined();
    expect(map.get(0)?.words12).toEqual(SOURCE.words12.slice(0, 20));
  });

  it('clamps slice ranges that overshoot the source length', () => {
    const map = captureSliceAudio(
      SOURCE,
      [{ start: 90, length: 50 }], // 90..140 — past the 100-word tail
      [3],
    );
    expect(map.get(3)?.words12).toHaveLength(10); // 90..100
    expect(map.get(3)?.words12).toEqual(SOURCE.words12.slice(90, 100));
  });

  it('truncates to the shorter of slices vs destSlots', () => {
    const map = captureSliceAudio(
      SOURCE,
      [
        { start: 0,  length: 25 },
        { start: 25, length: 25 },
        { start: 50, length: 50 },
      ],
      [1, 2], // only two slots for three slices
    );
    expect(map.size).toBe(2);
    expect(map.has(1)).toBe(true);
    expect(map.has(2)).toBe(true);
  });

  it('returns an empty map when source is empty', () => {
    const map = captureSliceAudio(
      { words12: [] },
      [{ start: 0, length: 100 }],
      [0],
    );
    expect(map.get(0)?.words12).toEqual([]);
  });

  it('handles negative start/length by clamping (defensive, not a use case)', () => {
    const map = captureSliceAudio(
      SOURCE,
      [{ start: -10, length: -5 }],
      [0],
    );
    expect(map.get(0)?.words12).toEqual([]);
  });
});

describe('attachAudioToSamples', () => {
  it('overlays audio onto matching slots and leaves others untouched', () => {
    const list: Sample[] = [
      newLocalSample(0, 'A', 26040, 100),
      newLocalSample(1, 'B', 26040, 50),
      newLocalSample(2, 'C', 26040, 75),
    ];
    const map = new Map([
      [0, { pcm: [0.1, 0.2], words12: [1, 2] }],
      [2, { pcm: [0.3, 0.4], words12: [3, 4] }],
    ]);
    const next = attachAudioToSamples(list, map);

    expect(next[0].words12).toEqual([1, 2]);
    expect(next[0].pcm).toEqual([0.1, 0.2]);
    expect(next[1].words12).toBeUndefined();   // unchanged
    expect(next[2].words12).toEqual([3, 4]);
  });

  it('does not mutate the input list', () => {
    const list: Sample[] = [newLocalSample(0, 'A', 26040, 100)];
    const map = new Map([[0, { pcm: [0.5], words12: [42] }]]);
    const next = attachAudioToSamples(list, map);
    expect(next).not.toBe(list);
    expect(list[0].pcm).toBeUndefined(); // original untouched
  });

  it('returns a shallow copy when the audio map is empty', () => {
    const list: Sample[] = [newLocalSample(0, 'A', 26040, 100)];
    const next = attachAudioToSamples(list, new Map());
    expect(next).toEqual(list);
    expect(next).not.toBe(list);
  });

  it('round-trips captureSliceAudio → attachAudioToSamples', () => {
    // The end-to-end Phase 1A invariant: capture a parent's audio,
    // apply (simulated), refreshCatalog drops in device rows at the
    // same slots, attach restores the audio.
    const parentPcm     = Array.from({ length: 200 }, (_, i) => i * 0.01);
    const parentWords12 = Array.from({ length: 200 }, (_, i) => i);
    const slices = [
      { start: 0,  length: 100 },
      { start: 100, length: 100 },
    ];
    const destSlots = [0, 1];

    const captured = captureSliceAudio(
      { pcm: parentPcm, words12: parentWords12 },
      slices,
      destSlots,
    );

    // Simulated post-refreshCatalog state: fresh device rows, no audio.
    const fresh: Sample[] = destSlots.map((slot, i) => ({
      ...newLocalSample(slot, `CHILD_${i}`, 26040, slices[i].length),
      source: 'device',
    }));
    const reattached = attachAudioToSamples(fresh, captured);

    // Both children carry the right subarrays of the parent.
    expect(reattached[0].words12).toEqual(parentWords12.slice(0, 100));
    expect(reattached[0].pcm).toEqual(parentPcm.slice(0, 100));
    expect(reattached[1].words12).toEqual(parentWords12.slice(100, 200));
    expect(reattached[1].pcm).toEqual(parentPcm.slice(100, 200));
    // source flag is preserved (we only patch audio fields).
    expect(reattached[0].source).toBe('device');
    expect(reattached[1].source).toBe('device');
  });
});

describe('expectedSeconds', () => {
  it('returns one bar of 4/4 at 120 BPM as 2 seconds', () => {
    // 4 beats × 0.5 s/beat = 2 s. The division parameter is unused
    // by the formula (subdivisions of a beat don't change the bar's
    // length) — pinned here so a refactor doesn't sneak division in.
    expect(expectedSeconds(1, 4, 120)).toBe(2);
    expect(expectedSeconds(1, 8, 120)).toBe(2);
    expect(expectedSeconds(1, 16, 120)).toBe(2);
  });
  it('scales linearly with bars and inversely with BPM', () => {
    expect(expectedSeconds(4, 4, 120)).toBe(8);
    expect(expectedSeconds(1, 4, 60)).toBe(4);
  });
  it('returns 0 when bpm is non-positive (avoids div-by-zero in UI)', () => {
    expect(expectedSeconds(1, 4, 0)).toBe(0);
    expect(expectedSeconds(1, 4, -1)).toBe(0);
  });
});

describe('buildBeatSlices', () => {
  // 1 bar of 1/8 at 120 BPM @ 26040 Hz → each slice spans
  //   (60/120) * (4/8) = 0.25s = 6510 samples.
  // 8 slices total = 52,080 samples in the "expected" length.
  const BPM = 120, RATE = 26040;

  it('places slices at fixed beat-intervals when sample matches expected length', () => {
    const slices = buildBeatSlices(1, 8, BPM, RATE, 52080);
    expect(slices).toHaveLength(8);
    // First slice starts at 0; subsequent at multiples of samplesPerSlice.
    expect(slices[0].start).toBe(0);
    expect(slices[1].start).toBe(6510);
    expect(slices[2].start).toBe(13020);
    expect(slices[7].start).toBe(45570);
    // Each slice is samplesPerSlice long except the last, which
    // closes to the sample's actual end (here, identical to expected).
    expect(slices[0].length).toBe(6510);
    expect(slices[7].length).toBe(52080 - 45570);
  });

  it('truncates when the sample is shorter than bars × division', () => {
    // Sample is only ~5 slices' worth of audio. We get 5 slices,
    // not 8 — the trailing 3 expected slices would start past
    // sampleLength so they're dropped.
    const slices = buildBeatSlices(1, 8, BPM, RATE, 6510 * 5);
    expect(slices).toHaveLength(5);
    expect(slices[4].length).toBe(6510);
  });

  it('does not extend the last slice past sampleLength when sample is longer', () => {
    // Sample is 1.5x expected. We still get 8 slices @ 6510 each;
    // the trailing audio falls outside any slice (user can extend
    // bars to capture it).
    const slices = buildBeatSlices(1, 8, BPM, RATE, Math.floor(52080 * 1.5));
    expect(slices).toHaveLength(8);
    expect(slices[7].start + slices[7].length).toBeLessThanOrEqual(52080 + 1);
  });

  it('falls back to even-division when bpm or rate is zero', () => {
    // Mirrors buildEvenSlices when we lack the inputs for beat math
    // — same n, same total length coverage.
    const beats = buildBeatSlices(1, 8, 0, RATE, 1000);
    const even  = buildEvenSlices(slicesFor(1, 8), 1000);
    expect(beats).toEqual(even);
  });

  it('caps slice count at MAX_SLICES', () => {
    // bars × division would be 64 (== MAX_SLICES); push it past.
    const slices = buildBeatSlices(8, 32, BPM, RATE, 1_000_000);
    expect(slices.length).toBeLessThanOrEqual(MAX_SLICES);
  });
});

describe('findZeroCrossing', () => {
  it('returns the target index when it already sits on a crossing', () => {
    // Adjacent samples [1, -1] cross at index 0 (sign change between 0 and 1).
    expect(findZeroCrossing([1, -1, -1, -1], 0, 10)).toBe(0);
  });

  it('searches outward from the target index', () => {
    //                     idx: 0  1  2  3  4  5
    const samples         =   [  1, 1, 1, 1, -1, 1];
    // Crossings sit at index 3 (sign change between samples[3]=1 and
    // samples[4]=-1) and index 4 (samples[4]=-1 → samples[5]=1).
    // Targeting index 2 with r=1 checks index 1 (no) then index 3
    // (yes) — returns 3 because the left-neighbour check happens
    // first within a given radius.
    expect(findZeroCrossing(samples, 2, 5)).toBe(3);
  });

  it('respects the zeroLevel for S950 offset-binary words', () => {
    // S950 words: silence = 2048. Sign change is around 2048, not 0.
    //                      idx: 0     1     2     3     4
    const words            =   [3000, 3000, 2000, 2000, 3000];
    // Targeting index 0 with zeroLevel=2048: crossing between (1,2) since
    // 3000-2048=+952 and 2000-2048=-48 → sign change.
    expect(findZeroCrossing(words, 0, 10, 2048)).toBe(1);
  });

  it('returns the target unchanged when no crossing is in range', () => {
    // All-positive run with a tiny search radius — no crossing visible.
    expect(findZeroCrossing([1, 1, 1, 1, 1], 2, 1)).toBe(2);
  });

  it('returns the target when the audio buffer is missing or too short', () => {
    expect(findZeroCrossing(undefined, 5, 10)).toBe(5);
    expect(findZeroCrossing([], 5, 10)).toBe(5);
    expect(findZeroCrossing([42], 5, 10)).toBe(5);
  });
});
