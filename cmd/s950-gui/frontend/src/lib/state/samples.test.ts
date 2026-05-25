import { describe, it, expect, beforeEach } from 'vitest';
import { get } from 'svelte/store';
import {
  samples,
  selectedSampleSlot,
  selectedSample,
  newLocalSample,
  pickFreeSlot,
  type Sample,
} from './samples';

beforeEach(() => {
  samples.set([]);
  selectedSampleSlot.set(0);
});

describe('newLocalSample', () => {
  it('defaults to source: "local"', () => {
    const s = newLocalSample(3, 'KICK', 26040, 1000);
    expect(s.source).toBe('local');
  });
  it('places the end marker at the sample length', () => {
    const s = newLocalSample(0, 'X', 26040, 500);
    expect(s.start).toBe(0);
    expect(s.end).toBe(500);
  });
  it('treats omitted name as empty', () => {
    const s = newLocalSample(0);
    expect(s.name).toBe('');
    expect(s.length).toBe(0);
  });
});

describe('pickFreeSlot', () => {
  it('returns 0 for an empty list', () => {
    expect(pickFreeSlot([])).toBe(0);
  });
  it('returns the lowest gap', () => {
    const list = [0, 1, 2, 4, 5].map((n) => newLocalSample(n));
    expect(pickFreeSlot(list)).toBe(3);
  });
  it('walks past the highest occupied slot when there is no gap', () => {
    const list = [0, 1, 2].map((n) => newLocalSample(n));
    expect(pickFreeSlot(list)).toBe(3);
  });
  it('returns -1 when all 100 slots are taken', () => {
    const list = Array.from({ length: 100 }, (_, i) => newLocalSample(i));
    expect(pickFreeSlot(list)).toBe(-1);
  });
});

describe('selectedSample', () => {
  it('returns undefined when the samples list is empty', () => {
    expect(get(selectedSample)).toBeUndefined();
  });

  it('returns the sample matching the selected slot', () => {
    const a: Sample = newLocalSample(2, 'TWO', 26040, 100);
    const b: Sample = newLocalSample(7, 'SEVEN', 26040, 100);
    samples.set([a, b]);
    selectedSampleSlot.set(7);
    expect(get(selectedSample)?.name).toBe('SEVEN');
  });

  it('falls back to the first list entry when the slot is missing', () => {
    samples.set([newLocalSample(5, 'FIVE', 26040, 100)]);
    selectedSampleSlot.set(42); // not present
    expect(get(selectedSample)?.slot).toBe(5);
  });

  it('updates patch the selected row via store.update', () => {
    samples.set([newLocalSample(0, 'A', 26040, 100)]);
    selectedSampleSlot.set(0);
    selectedSample.update({ name: 'B' });
    expect(get(samples)[0].name).toBe('B');
  });

  it('only mutates the selected row, not its siblings', () => {
    samples.set([
      newLocalSample(0, 'A', 26040, 100),
      newLocalSample(1, 'B', 26040, 100),
    ]);
    selectedSampleSlot.set(1);
    selectedSample.update({ name: 'B2' });
    const list = get(samples);
    expect(list[0].name).toBe('A');
    expect(list[1].name).toBe('B2');
  });
});
