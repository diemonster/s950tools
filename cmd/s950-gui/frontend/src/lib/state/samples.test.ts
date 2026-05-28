import { describe, it, expect, beforeEach } from 'vitest';
import { get } from 'svelte/store';
import {
  samples,
  selectedSampleSlot,
  selectedSample,
  newLocalSample,
  pickFreeSlot,
  removeLocalSample,
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

// deviceSnapshot captures the pre-edit device-coherent state so
// revertSampleToDevice can restore byte-identically. The capture
// rule is subtle: only fire on the FIRST edit to a sample that is
// currently synced with the device (source === 'device' AND
// originalSource === 'device'). These tests pin the contract; a
// regression here breaks the sidebar's revert affordance silently.

function deviceSample(slot: number, patch: Partial<Sample> = {}): Sample {
  // newLocalSample defaults source='local', originalSource='local'.
  // Override both to mimic a sample loaded from the device's
  // catalog (the production code does this via skinnySample /
  // sampleParamsToSample).
  return {
    ...newLocalSample(slot, `S${slot}`, 26040, 100),
    source: 'device',
    originalSource: 'device',
    ...patch,
  };
}

describe('selectedSample.update — deviceSnapshot capture', () => {
  it('captures pre-edit state on FIRST edit to a synced device sample', () => {
    samples.set([deviceSample(0, { tune: 0 })]);
    selectedSampleSlot.set(0);
    selectedSample.update({ tune: 5 });

    const updated = get(samples)[0];
    expect(updated.tune).toBe(5); // patch applied
    expect(updated.deviceSnapshot).toBeDefined();
    expect(updated.deviceSnapshot?.tune).toBe(0); // snapshot pre-edit
  });

  it('does not re-capture on subsequent edits — snapshot is pre-FIRST-edit', () => {
    samples.set([deviceSample(0, { tune: 0 })]);
    selectedSampleSlot.set(0);
    selectedSample.update({ tune: 5 });
    selectedSample.update({ tune: 10 });

    const s = get(samples)[0];
    expect(s.tune).toBe(10);
    // Snapshot stays at the ORIGINAL (0), not the intermediate (5).
    expect(s.deviceSnapshot?.tune).toBe(0);
  });

  it('captures PCM and words12 alongside the SPRM fields', () => {
    // Critical for the resample case: snapshot must hold the
    // pre-resample audio so revert restores it (we can't re-fetch
    // host PCM from the device cheaply).
    samples.set([deviceSample(0, {
      pcm: [100, 200, 300],
      words12: [0x800, 0x900, 0xA00],
      rate: 44100,
    })]);
    selectedSampleSlot.set(0);
    selectedSample.update({
      rate: 22050,
      pcm: [50],
      words12: [0x800],
      source: 'local',
    });

    const snap = get(samples)[0].deviceSnapshot;
    expect(snap?.rate).toBe(44100);
    expect(snap?.pcm).toEqual([100, 200, 300]);
    expect(snap?.words12).toEqual([0x800, 0x900, 0xA00]);
  });

  it('skips capture for purely-local samples (no device counterpart)', () => {
    // newLocalSample defaults are source='local' and
    // originalSource='local'. An edit shouldn't fabricate a
    // snapshot of "what was on the device" because nothing was.
    samples.set([newLocalSample(0, 'IMPORT', 26040, 100)]);
    selectedSampleSlot.set(0);
    selectedSample.update({ tune: 5 });

    expect(get(samples)[0].deviceSnapshot).toBeUndefined();
  });

  it('skips capture when source is already local (mid-edit re-update)', () => {
    // If a previous edit already flipped source to 'local' but
    // never captured a snapshot for some reason, a subsequent
    // edit shouldn't backfill — the "pre-edit truth" is already
    // lost. Defensive against state-machine confusion.
    samples.set([{
      ...deviceSample(0),
      source: 'local', // already diverged
    }]);
    selectedSampleSlot.set(0);
    selectedSample.update({ tune: 5 });

    expect(get(samples)[0].deviceSnapshot).toBeUndefined();
  });

  it('respects an explicit deviceSnapshot: undefined override in the patch', () => {
    // Send-to-S950 success path passes { deviceSnapshot: undefined }
    // to clear the snapshot (local state is now the device state).
    // The capture-on-first-edit logic must not override that clear.
    samples.set([{
      ...deviceSample(0, { tune: 5 }),
      deviceSnapshot: { ...deviceSample(0, { tune: 0 }) },
    }]);
    selectedSampleSlot.set(0);
    selectedSample.update({ source: 'device', deviceSnapshot: undefined });

    expect(get(samples)[0].deviceSnapshot).toBeUndefined();
  });
});

describe('removeLocalSample', () => {
  // Convenience: a sample marked as living on the device. removeLocal
  // must refuse to touch these — the S950 has no remote-delete
  // SysEx, so dropping a row would silently disagree with the catalog.
  const deviceSample = (slot: number, name: string): Sample => ({
    ...newLocalSample(slot, name, 26040, 100),
    source: 'device',
  });

  it('removes a local sample and reports true', () => {
    samples.set([newLocalSample(0, 'A'), newLocalSample(1, 'B')]);
    expect(removeLocalSample(0)).toBe(true);
    const slots = get(samples).map((s) => s.slot);
    expect(slots).toEqual([1]);
  });

  it('refuses to remove a device sample', () => {
    samples.set([deviceSample(0, 'A')]);
    expect(removeLocalSample(0)).toBe(false);
    expect(get(samples)).toHaveLength(1);
  });

  it('returns false for an unknown slot', () => {
    samples.set([newLocalSample(0, 'A')]);
    expect(removeLocalSample(99)).toBe(false);
    expect(get(samples)).toHaveLength(1);
  });

  it('moves selection forward when deleting the active row', () => {
    samples.set([newLocalSample(0, 'A'), newLocalSample(1, 'B'), newLocalSample(2, 'C')]);
    selectedSampleSlot.set(1);
    removeLocalSample(1);
    // Index 1 was B; remaining list is [A, C] — selection lands on
    // whatever is at index 1 of the new list (C).
    expect(get(selectedSampleSlot)).toBe(2);
  });

  it('falls back to the previous row when deleting the last entry', () => {
    samples.set([newLocalSample(0, 'A'), newLocalSample(1, 'B')]);
    selectedSampleSlot.set(1);
    removeLocalSample(1);
    expect(get(selectedSampleSlot)).toBe(0);
  });

  it('leaves selection on slot 0 when the list ends up empty', () => {
    samples.set([newLocalSample(7, 'only')]);
    selectedSampleSlot.set(7);
    removeLocalSample(7);
    expect(get(samples)).toHaveLength(0);
    expect(get(selectedSampleSlot)).toBe(0);
  });

  it('does not move selection when deleting a non-active row', () => {
    samples.set([newLocalSample(0, 'A'), newLocalSample(1, 'B')]);
    selectedSampleSlot.set(0);
    removeLocalSample(1);
    expect(get(selectedSampleSlot)).toBe(0);
  });
});
