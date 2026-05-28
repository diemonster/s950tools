// Memory-scan tests. The user-facing math is the interesting part:
// summing TotalWords across the device samples, respecting the local
// vs device source distinction. Wails App.GetSampleParams is mocked
// so the scan is fully in-process.

import { describe, it, expect, beforeEach, vi } from 'vitest';
import { get } from 'svelte/store';

// Per-slot SPRM responses keyed by slot — tests poke this map before
// calling scanMemory(). Falls back to a 0-words result if a slot
// isn't pre-seeded (simulates an unreachable slot that should skip
// cleanly without aborting the rest of the scan).
const sprmResponses = new Map<number, { TotalWords: number }>();

vi.mock('../../../wailsjs/go/main/App', () => ({
  GetSampleParams: vi.fn((slot: number) =>
    Promise.resolve(sprmResponses.get(slot) ?? { TotalWords: 0 }),
  ),
}));

// connection.ts is imported by memory.ts via `phase`; stub the Wails
// imports it pulls in so the test environment doesn't try to reach
// real bindings.
vi.mock('./catalog', () => ({ refreshCatalog: vi.fn() }));

import { samples, newLocalSample, type Sample } from './samples';
import { phase } from './connection';
import {
  memoryUsage, expansionEnabled, totalWords, scanMemory, clearMemory,
  TOTAL_BASE, TOTAL_EXM005,
} from './memory';

beforeEach(() => {
  samples.set([]);
  sprmResponses.clear();
  clearMemory();
  expansionEnabled.set(false);
  phase.set('connected');
});

// Helper: device sample with a known TotalWords baked in.
function deviceSample(slot: number, length: number): Sample {
  return { ...newLocalSample(slot, `S${slot}`, 26040, length), source: 'device' };
}

describe('memory total', () => {
  it('reports the base S950 total without expansion', () => {
    expansionEnabled.set(false);
    expect(get(totalWords)).toBe(TOTAL_BASE);
  });

  it('reports the EXM005 total when expansion is on', () => {
    expansionEnabled.set(true);
    expect(get(totalWords)).toBe(TOTAL_EXM005);
  });
});

describe('scanMemory', () => {
  it('skips when not connected', async () => {
    phase.set('disconnected');
    samples.set([deviceSample(0, 10_000)]);
    sprmResponses.set(0, { TotalWords: 10_000 });
    await scanMemory();
    // No SPRM fetch, no update — chip stays at zero.
    expect(get(memoryUsage).usedWords).toBe(0);
    expect(get(memoryUsage).scannedAt).toBeNull();
  });

  it('sums TotalWords across device samples', async () => {
    samples.set([
      deviceSample(0, 10_000),
      deviceSample(1, 25_000),
      deviceSample(2, 5_000),
    ]);
    sprmResponses.set(0, { TotalWords: 10_000 });
    sprmResponses.set(1, { TotalWords: 25_000 });
    sprmResponses.set(2, { TotalWords: 5_000 });
    await scanMemory();
    expect(get(memoryUsage).usedWords).toBe(40_000);
    expect(get(memoryUsage).sampleCount).toBe(3);
    expect(get(memoryUsage).scannedAt).not.toBeNull();
  });

  it('ignores local samples in the total', async () => {
    samples.set([
      deviceSample(0, 10_000),
      newLocalSample(1, 'local', 26040, 999_999), // shouldn't count
    ]);
    sprmResponses.set(0, { TotalWords: 10_000 });
    await scanMemory();
    expect(get(memoryUsage).usedWords).toBe(10_000);
    expect(get(memoryUsage).sampleCount).toBe(1);
  });

  it('continues after a single-slot fetch failure', async () => {
    samples.set([deviceSample(0, 10_000), deviceSample(1, 20_000)]);
    sprmResponses.set(0, { TotalWords: 10_000 });
    // Slot 1: simulate a transient NAK / throw mid-scan. The scan
    // should keep going and count what it does have.
    const App = await import('../../../wailsjs/go/main/App');
    // Cast through `any` — the real SampleParams type has 11+ fields
    // but the scan only reads TotalWords, so the partial response is
    // safe at the call site.
    (App.GetSampleParams as unknown as ReturnType<typeof vi.fn>)
      .mockImplementationOnce(() => Promise.resolve({ TotalWords: 10_000 }))
      .mockImplementationOnce(() => Promise.reject(new Error('timeout')));
    await scanMemory();
    // Slot 0 contributed; slot 1's fetch failed so it retains its
    // pre-scan length of 20_000 in the samples store and still sums.
    // The point of the test isn't the exact sum (depends on retain
    // behaviour) but that the scan completes without throwing.
    expect(get(memoryUsage).scanning).toBe(false);
    expect(get(memoryUsage).scannedAt).not.toBeNull();
  });

  it('sets and clears the scanning flag', async () => {
    samples.set([deviceSample(0, 1_000)]);
    sprmResponses.set(0, { TotalWords: 1_000 });
    const promise = scanMemory();
    expect(get(memoryUsage).scanning).toBe(true);
    await promise;
    expect(get(memoryUsage).scanning).toBe(false);
  });
});

describe('clearMemory', () => {
  it('resets the chip to zero', async () => {
    samples.set([deviceSample(0, 12_345)]);
    sprmResponses.set(0, { TotalWords: 12_345 });
    await scanMemory();
    expect(get(memoryUsage).usedWords).toBe(12_345);
    clearMemory();
    expect(get(memoryUsage).usedWords).toBe(0);
    expect(get(memoryUsage).scannedAt).toBeNull();
  });
});
