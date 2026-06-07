// Tests for the program-level mutators. selectedProgram.addKeygroup
// is the headline case (it powers the canvas-head "Add Zone" button);
// the test also pins that the constant matches the protocol's hard
// 31-keygroup limit so a future bump on either side breaks loudly.

import { describe, it, expect, beforeEach, vi } from 'vitest';
import { get } from 'svelte/store';
import {
  programs, selectedSlot, selectedProgram,
  newLocalProgram, MAX_KEYGROUPS, ZONE_COLORS,
} from './programs';

// scheduleProgramWriteback fires off a backend SetProgram via a lazy
// import — mock the whole livesync module so addKeygroup can run
// without a connected device.
vi.mock('./livesync', () => ({
  scheduleProgramWriteback: vi.fn(),
}));

beforeEach(() => {
  programs.set([]);
  selectedSlot.set(0);
  vi.clearAllMocks();
});

describe('MAX_KEYGROUPS', () => {
  it('matches the S950 firmware cap (31)', () => {
    // Pinned to internal/protocol/program.go:MaxKeygroups. If the
    // protocol layer ever changes, this test forces the frontend
    // constant to update too — silent drift would let the UI offer
    // an Add Zone past the cap and the device would NAK on Apply.
    expect(MAX_KEYGROUPS).toBe(31);
  });
});

describe('selectedProgram.addKeygroup', () => {
  it('appends a keygroup and returns its 1-based index', () => {
    programs.set([newLocalProgram(0, 'TEST')]);
    selectedSlot.set(0);

    const n = selectedProgram.addKeygroup();

    expect(n).toBe(2);
    const list = get(programs);
    expect(list[0].keygroups).toHaveLength(2);
    expect(list[0].keygroups[1].n).toBe(2);
  });

  it('cycles the color palette by keygroup count', () => {
    programs.set([newLocalProgram(0, 'TEST')]);
    selectedSlot.set(0);

    // First add → index 1 in the palette (second slot).
    selectedProgram.addKeygroup();
    selectedProgram.addKeygroup();

    const kgs = get(programs)[0].keygroups;
    // Initial seed keygroup uses the newKeygroup default color (yellow).
    // Subsequent adds index into ZONE_COLORS by current length.
    expect(kgs[1].color).toBe(ZONE_COLORS[1]);
    expect(kgs[2].color).toBe(ZONE_COLORS[2]);
  });

  it('returns null and adds nothing when the program is at MAX_KEYGROUPS', () => {
    const p = newLocalProgram(0, 'FULL');
    // Pad up to MAX_KEYGROUPS — newLocalProgram seeds with one, so
    // we add MAX_KEYGROUPS - 1 more to reach the cap.
    for (let i = 2; i <= MAX_KEYGROUPS; i++) {
      p.keygroups.push({ ...p.keygroups[0], n: i });
    }
    programs.set([p]);
    selectedSlot.set(0);

    const before = get(programs)[0].keygroups.length;
    const n = selectedProgram.addKeygroup();

    expect(n).toBeNull();
    expect(get(programs)[0].keygroups).toHaveLength(before);
  });

  it('returns null when no program is selected', () => {
    programs.set([]); // empty bank — selectedProgram resolves to undefined
    const n = selectedProgram.addKeygroup();
    expect(n).toBeNull();
  });

  it('schedules a writeback so the new keygroup hits the device', async () => {
    programs.set([newLocalProgram(7, 'WRITE')]);
    selectedSlot.set(7);

    selectedProgram.addKeygroup();

    // Lazy-imported inside addKeygroup; wait one microtask tick for
    // the dynamic import's then() to flush before asserting.
    await Promise.resolve();
    await Promise.resolve();
    const livesync = await import('./livesync');
    expect(livesync.scheduleProgramWriteback).toHaveBeenCalledWith(7);
  });
});
