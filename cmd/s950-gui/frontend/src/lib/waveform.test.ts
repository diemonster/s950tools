// Pure-function tests for the waveform path builder. Visual
// correctness is verified manually in the dev app; this suite locks
// in the math that drives the SVG path strings.

import { describe, it, expect } from 'vitest';
import { buildWaveformPaths, buildSyntheticPaths } from './waveform';

// Helper: pull every Y coordinate out of an SVG path. The builder
// emits commands as `L x,y` so we can match decimal pairs and just
// pluck the second number.
function extractYs(path: string): number[] {
  const out: number[] = [];
  for (const m of path.matchAll(/[ML]\s*\d+(?:\.\d+)?,(-?\d+(?:\.\d+)?)/g)) {
    out.push(parseFloat(m[1]));
  }
  return out;
}

describe('buildWaveformPaths', () => {
  it('returns a flat midline when given empty PCM', () => {
    const { top, bot } = buildWaveformPaths([], 0, 0, 100, 50, 40);
    expect(top).toContain('M 0,50');
    expect(bot).toContain('M 0,50');
    // No fluctuation — every Y in the path equals the center.
    expect(extractYs(top).every((y) => y === 50)).toBe(true);
  });

  it('flat midline when the visible window misses the audio', () => {
    // viewStart past the end of pcm — peak aggregation should
    // gracefully return centerY, not throw.
    const { top } = buildWaveformPaths([100, 200], 50, 10, 100, 50, 40);
    expect(extractYs(top).every((y) => y === 50)).toBe(true);
  });

  it('top stays at-or-above centerY, bot stays at-or-below', () => {
    // Mix of ±15000 samples so we get both peaks and troughs.
    const pcm: number[] = [];
    for (let i = 0; i < 1000; i++) pcm.push((i % 2 === 0 ? 1 : -1) * 15000);

    const { top, bot } = buildWaveformPaths(pcm, 0, 1000, 200, 100, 90);
    // Skip the leading M and closing Z by re-extracting numbers.
    for (const y of extractYs(top))  expect(y).toBeLessThanOrEqual(100);
    for (const y of extractYs(bot))  expect(y).toBeGreaterThanOrEqual(100);
  });

  it('detects the peak of a known waveform', () => {
    // Single positive spike at sample 500, otherwise zero.
    const pcm = new Array(1000).fill(0);
    pcm[500] = 32000; // ≈ full-scale positive
    const { top } = buildWaveformPaths(pcm, 0, 1000, 200, 100, 90);
    // The top path should reach close to its highest point (lowest Y
    // value) — full-scale of 32000/32768 * 90 = ~87.9 below centerY,
    // i.e. y ≈ 12. Allow a couple of px slack for rounding.
    const minY = Math.min(...extractYs(top));
    expect(minY).toBeLessThan(20);
  });

  it('produces one peak pair per output column', () => {
    const pcm = new Array(500).fill(10000);
    const { top } = buildWaveformPaths(pcm, 0, 500, 50, 100, 90);
    // 50 columns → 50 L commands plus the leading M and trailing
    // closing L — so 52 total numeric pairs.
    const ys = extractYs(top);
    expect(ys.length).toBe(52);
  });

  it('zooming into a window re-renders against that window only', () => {
    // First half is loud (32000), second half is silence.
    const pcm = new Array(1000).fill(0).map((_, i) => (i < 500 ? 32000 : 0));
    const full  = buildWaveformPaths(pcm, 0,   1000, 100, 100, 90);
    const right = buildWaveformPaths(pcm, 500, 500,  100, 100, 90);
    // The right-only path should be flat at center (all silence),
    // while the full path has a tall left half. Compare maxima:
    expect(Math.min(...extractYs(full.top))).toBeLessThan(20);
    expect(Math.min(...extractYs(right.top))).toBeGreaterThan(95);
  });
});

describe('buildSyntheticPaths', () => {
  it('is deterministic for the same slot', () => {
    const a = buildSyntheticPaths(5);
    const b = buildSyntheticPaths(5);
    expect(a.top).toBe(b.top);
    expect(a.bot).toBe(b.bot);
  });

  it('different slots produce different paths', () => {
    const a = buildSyntheticPaths(1);
    const b = buildSyntheticPaths(2);
    expect(a.top).not.toBe(b.top);
  });
});
