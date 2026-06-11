// Tests for planProgramSend — the never-overwrite send planner that
// replaced the mockup preflight modal. Policy locked in here:
// occupied device slots are NEVER overwritten (hardware-tested:
// occupied-slot writes NAK unreliably and the S950 has no remote
// delete); local programs and samples re-target to free slots.

import { describe, it, expect } from 'vitest';
import { planProgramSend, referencedSampleNames } from './sendplan';
import { newLocalProgram, newKeygroup, type Program } from './programs';
import { newLocalSample, type Sample } from './samples';

const FREE_WORDS = 500_000;
const BAUD = 50000;

function progWithSamples(slot: number, names: string[], source: Program['source'] = 'local'): Program {
  const p = newLocalProgram(slot, 'TESTKIT');
  p.keygroups = names.map((n, i) => {
    const kg = newKeygroup(i + 1);
    kg.soft.sample = n;
    return kg;
  });
  return { ...p, source };
}

function deviceProgram(slot: number, name: string): Program {
  return { ...newLocalProgram(slot, name), source: 'device' };
}

function deviceSample(slot: number, name: string): Sample {
  return { ...newLocalSample(slot, name, 26040, 100), source: 'device' };
}

function localSampleWithAudio(slot: number, name: string, words = 1000): Sample {
  const s = newLocalSample(slot, name, 26040, words);
  return { ...s, source: 'local', words12: new Array(words).fill(0x800) };
}

describe('planProgramSend — program slot policy', () => {
  it('device program updates its own slot, no warnings', () => {
    const plan = planProgramSend(
      progWithSamples(5, ['KICK'], 'device'),
      [deviceProgram(5, 'TESTKIT')],
      [deviceSample(0, 'KICK')],
      FREE_WORDS, BAUD,
    );
    expect(plan.ok).toBe(true);
    expect(plan.programSlot).toBe(5);
    expect(plan.slotReassigned).toBe(false);
  });

  it('local program on a free slot keeps it', () => {
    const plan = planProgramSend(
      progWithSamples(7, ['KICK']),
      [deviceProgram(0, 'OTHER')],
      [deviceSample(0, 'KICK')],
      FREE_WORDS, BAUD,
    );
    expect(plan.programSlot).toBe(7);
    expect(plan.slotReassigned).toBe(false);
  });

  it('NEVER overwrites: local program colliding with a device slot re-targets to the lowest free slot', () => {
    const plan = planProgramSend(
      progWithSamples(0, ['KICK']),
      [deviceProgram(0, 'PRECIOUS'), deviceProgram(1, 'ALSO')],
      [deviceSample(0, 'KICK')],
      FREE_WORDS, BAUD,
    );
    expect(plan.ok).toBe(true);
    expect(plan.slotReassigned).toBe(true);
    expect(plan.programSlot).toBe(2); // lowest free
    const warnItem = plan.items.find((i) => i.kind === 'warn' && i.title.includes('occupied'));
    expect(warnItem).toBeTruthy();
    expect(warnItem!.title).toContain('PRECIOUS');
    // No overwrite affordance exists anywhere in the plan.
    expect(plan.items.some((i) => /overwrit/i.test(i.title))).toBe(false);
  });

  it('errors when all 100 program slots are device-occupied', () => {
    const all = Array.from({ length: 100 }, (_, i) => deviceProgram(i, `P${i}`));
    const plan = planProgramSend(
      progWithSamples(0, ['KICK']), all, [deviceSample(0, 'KICK')],
      FREE_WORDS, BAUD,
    );
    expect(plan.ok).toBe(false);
  });
});

describe('planProgramSend — referenced samples', () => {
  it('samples already on device need no upload', () => {
    const plan = planProgramSend(
      progWithSamples(1, ['KICK', 'SNARE']),
      [],
      [deviceSample(0, 'KICK'), deviceSample(1, 'SNARE')],
      FREE_WORDS, BAUD,
    );
    expect(plan.uploads).toHaveLength(0);
    expect(plan.missing).toHaveLength(0);
  });

  it('local samples with audio queue for upload to FREE slots only', () => {
    const plan = planProgramSend(
      progWithSamples(1, ['LOCALKICK']),
      [],
      // Device occupies slot 0 → upload claims the lowest FREE
      // slot (1) — never an occupied one.
      [deviceSample(0, 'OTHER'), localSampleWithAudio(0, 'LOCALKICK', 2000)],
      FREE_WORDS, BAUD,
    );
    expect(plan.uploads).toHaveLength(1);
    expect(plan.uploads[0].toSlot).toBe(1); // not 0 — never overwrite
    expect(plan.uploads[0].words).toBe(2000);
    expect(plan.uploadWords).toBe(2000);
  });

  it('always targets the LOWEST free slot, not the local row slot', () => {
    // Hardware-observed: the S950's open-loop receive appends at its
    // own lowest free slot regardless of the dump header. The local
    // row sits at 7, but with the device empty the sample will land
    // at 0 — and our SPRM write must follow it there.
    const plan = planProgramSend(
      progWithSamples(1, ['LOCALKICK']),
      [],
      [localSampleWithAudio(7, 'LOCALKICK')],
      FREE_WORDS, BAUD,
    );
    expect(plan.uploads[0].toSlot).toBe(0);
    expect(plan.uploads[0].fromSlot).toBe(7);
  });

  it('allocates sequentially from the lowest free slot past device samples', () => {
    // Device occupies 0..1 → uploads land at 2, 3 in send order,
    // mirroring the device's own append behavior.
    const plan = planProgramSend(
      progWithSamples(1, ['A', 'B']),
      [],
      [
        deviceSample(0, 'X'), deviceSample(1, 'Y'),
        localSampleWithAudio(9, 'A'), localSampleWithAudio(8, 'B'),
      ],
      FREE_WORDS, BAUD,
    );
    expect(plan.uploads.map((u) => u.toSlot)).toEqual([2, 3]);
  });

  it('two uploads never claim the same free slot', () => {
    const plan = planProgramSend(
      progWithSamples(1, ['A', 'B']),
      [],
      [
        deviceSample(0, 'OTHER'),
        localSampleWithAudio(0, 'A'),
        { ...localSampleWithAudio(1, 'B'), slot: 0 + 100 - 100 }, // also slot 0 collision
      ].map((s, i) => (i === 2 ? { ...s, slot: 0 } : s)),
      FREE_WORDS, BAUD,
    );
    expect(plan.uploads).toHaveLength(2);
    expect(plan.uploads[0].toSlot).not.toBe(plan.uploads[1].toSlot);
  });

  it('names with no audio anywhere are warnings, not errors', () => {
    const plan = planProgramSend(
      progWithSamples(1, ['GHOST']),
      [], [],
      FREE_WORDS, BAUD,
    );
    expect(plan.ok).toBe(true); // sendable — keygroups just silent
    expect(plan.missing).toEqual(['GHOST']);
    expect(plan.items.some((i) => i.kind === 'warn' && i.title.includes('GHOST'))).toBe(true);
  });

  it('warns when the program references no samples at all', () => {
    const p = newLocalProgram(1, 'EMPTYKIT'); // one keygroup, blank bindings
    const plan = planProgramSend(p, [], [], FREE_WORDS, BAUD);
    expect(plan.items.some((i) => i.kind === 'warn' && i.title === 'No samples referenced')).toBe(true);
  });
});

describe('planProgramSend — memory + structure', () => {
  it('errors when uploads exceed free device memory', () => {
    const plan = planProgramSend(
      progWithSamples(1, ['BIG']),
      [],
      [localSampleWithAudio(0, 'BIG', 10_000)],
      5_000, BAUD,
    );
    expect(plan.ok).toBe(false);
    expect(plan.items.some((i) => i.kind === 'error' && i.title.includes('memory'))).toBe(true);
  });

  it('errors on zero keygroups', () => {
    const p = newLocalProgram(1, 'X');
    p.keygroups = [];
    expect(planProgramSend(p, [], [], FREE_WORDS, BAUD).ok).toBe(false);
  });

  it('estimate grows with upload size', () => {
    const small = planProgramSend(
      progWithSamples(1, ['A']), [], [localSampleWithAudio(0, 'A', 1000)],
      FREE_WORDS, BAUD,
    );
    const big = planProgramSend(
      progWithSamples(1, ['A']), [], [localSampleWithAudio(0, 'A', 100_000)],
      FREE_WORDS, BAUD,
    );
    expect(big.estSeconds).toBeGreaterThan(small.estSeconds);
  });
});

describe('referencedSampleNames', () => {
  it('collects unique trimmed soft+loud names', () => {
    const p = newLocalProgram(0, 'X');
    const kg1 = newKeygroup(1);
    kg1.soft.sample = 'KICK ';
    kg1.loud.sample = 'KICKHARD';
    const kg2 = newKeygroup(2);
    kg2.soft.sample = 'KICK'; // duplicate after trim
    p.keygroups = [kg1, kg2];
    expect(referencedSampleNames(p).sort()).toEqual(['KICK', 'KICKHARD']);
  });
});
