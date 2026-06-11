// Send-to-S950 preflight planner. Pure — computed entirely from the
// stores, no wire traffic — so the modal renders instantly and the
// logic is unit-testable (same pattern as the backend's planSlicing).
//
// POLICY (hardware-tested, see project notes): the S950 has no
// remote-delete and occupied-slot writes are unreliable (program
// writes to populated slots can NAK; sample-slot overwrites corrupt
// state). This planner therefore NEVER overwrites:
//   • a LOCAL program whose slot is taken on the device is silently
//     re-targeted to the lowest free program slot (surfaced as a
//     warning so the user knows where it landed);
//   • referenced samples that need uploading only ever go to FREE
//     sample slots;
//   • a DEVICE-sourced program updates its own slot — that's the
//     normal live-sync path, not an overwrite of foreign state.

import type { Program } from './programs';
import type { Sample } from './samples';

export type SendPlanItem = {
  kind: 'ok' | 'warn' | 'error';
  title: string;
  detail: string;
};

export type SendPlanUpload = {
  // Sample-store slot the audio currently lives at (for store
  // updates after a reassignment).
  fromSlot: number;
  // Device slot the upload will land in.
  toSlot: number;
  name: string;
  words: number;
};

export type SendPlan = {
  items: SendPlanItem[];
  ok: boolean;
  // Resolved destination program slot (differs from the program's
  // current slot when a local program collided with a device slot).
  programSlot: number;
  slotReassigned: boolean;
  uploads: SendPlanUpload[];
  // Referenced sample names with no audio anywhere — the program
  // will play silence on those keygroups.
  missing: string[];
  // Total words going over the wire (samples only).
  uploadWords: number;
  // Rough wall-clock estimate in seconds for uploads + program.
  estSeconds: number;
};

const MAX_KG = 31;

// referencedSampleNames collects the unique, non-empty soft/loud
// sample names a program's keygroups point at.
export function referencedSampleNames(p: Program): string[] {
  const names = new Set<string>();
  for (const kg of p.keygroups) {
    const soft = kg.soft.sample.trim();
    const loud = kg.loud.sample.trim();
    if (soft) names.add(soft);
    if (loud) names.add(loud);
  }
  return [...names];
}

export function planProgramSend(
  prog: Program,
  allPrograms: Program[],
  allSamples: Sample[],
  freeWords: number,
  baud: number,
): SendPlan {
  const plan: SendPlan = {
    items: [],
    ok: true,
    programSlot: prog.slot,
    slotReassigned: false,
    uploads: [],
    missing: [],
    uploadWords: 0,
    estSeconds: 0,
  };
  const err = (title: string, detail: string) => {
    plan.items.push({ kind: 'error', title, detail });
    plan.ok = false;
  };
  const warn = (title: string, detail: string) =>
    plan.items.push({ kind: 'warn', title, detail });
  const ok = (title: string, detail: string) =>
    plan.items.push({ kind: 'ok', title, detail });

  // ---- Keygroups ----
  if (prog.keygroups.length === 0) {
    err('No keygroups', 'Add at least one zone before sending.');
  } else if (prog.keygroups.length > MAX_KG) {
    err('Too many keygroups', `${prog.keygroups.length} keygroups — the S950 caps programs at ${MAX_KG}.`);
  } else {
    ok('Keygroup count', `${prog.keygroups.length} keygroup${prog.keygroups.length === 1 ? '' : 's'} · max ${MAX_KG}`);
  }

  // ---- Program slot ----
  const deviceProgSlots = new Map(
    allPrograms.filter((p) => p.source === 'device').map((p) => [p.slot, p.name]),
  );
  if (prog.source === 'device') {
    ok('Program slot', `Updates slot ${pad(prog.slot)} on the device (this program's own slot).`);
  } else if (!deviceProgSlots.has(prog.slot)) {
    ok('Program slot', `Stores to free slot ${pad(prog.slot)}.`);
  } else {
    // Local program colliding with a device slot: re-target to the
    // lowest free slot — never overwrite (occupied-slot writes NAK
    // unreliably on this hardware, and silent replacement of a
    // foreign program would be worse).
    let free = -1;
    for (let i = 0; i < 100; i++) {
      if (!deviceProgSlots.has(i)) { free = i; break; }
    }
    if (free < 0) {
      err('No free program slots', 'All 100 device program slots are occupied.');
    } else {
      plan.programSlot = free;
      plan.slotReassigned = true;
      warn(
        `Slot ${pad(prog.slot)} occupied (${deviceProgSlots.get(prog.slot)})`,
        `Sending to free slot ${pad(free)} instead — this app never overwrites occupied slots.`,
      );
    }
  }

  // ---- Referenced samples ----
  const deviceSamples = new Set(
    allSamples.filter((s) => s.source === 'device').map((s) => s.name.trim()),
  );
  const deviceSampleSlots = new Set(
    allSamples.filter((s) => s.source === 'device').map((s) => s.slot),
  );
  const localByName = new Map(
    allSamples
      .filter((s) => s.source === 'local' && s.words12 && s.words12.length > 0)
      .map((s) => [s.name.trim(), s]),
  );

  const refs = referencedSampleNames(prog);
  let onDevice = 0;
  const claimed = new Set<number>();
  // ALWAYS the lowest free slot, in upload order — hardware-observed
  // (2026-06-10 wire log): the S950's open-loop SDS receive IGNORES
  // the dump header's sample number and appends at its own lowest
  // free slot, naming the sample after the header number. Targeting
  // any other slot strands the follow-up SPRM write (the name/loop
  // metadata) on an empty slot. Allocating lowest-free in send order
  // keeps our SPRM target in lockstep with where the device actually
  // puts each sample.
  const freeSampleSlot = (): number => {
    for (let i = 0; i < 100; i++) {
      if (!deviceSampleSlots.has(i) && !claimed.has(i)) return i;
    }
    return -1;
  };

  for (const name of refs) {
    if (deviceSamples.has(name)) {
      onDevice++;
      continue;
    }
    const local = localByName.get(name);
    if (!local) {
      plan.missing.push(name);
      continue;
    }
    const slot = freeSampleSlot();
    if (slot < 0) {
      err('No free sample slots', `${name} needs a slot but all 100 are occupied.`);
      continue;
    }
    claimed.add(slot);
    plan.uploads.push({
      fromSlot: local.slot,
      toSlot: slot,
      name,
      words: local.words12!.length,
    });
    plan.uploadWords += local.words12!.length;
  }

  if (refs.length === 0) {
    warn('No samples referenced', 'Every keygroup has empty sample bindings — the program will be silent.');
  } else {
    const parts: string[] = [];
    if (onDevice > 0) parts.push(`${onDevice} already on device`);
    if (plan.uploads.length > 0) parts.push(`${plan.uploads.length} will upload to free slots`);
    ok('Referenced samples', parts.length > 0 ? parts.join(' · ') : `${refs.length} referenced`);
  }
  for (const name of plan.missing) {
    warn(`${name} not available`, 'Not on the device and no local audio — its keygroups will be silent.');
  }

  // ---- Memory ----
  if (plan.uploadWords > 0) {
    if (plan.uploadWords > freeWords) {
      err('Not enough sample memory',
        `${fmtWords(plan.uploadWords)} words needed · ${fmtWords(freeWords)} free on device.`);
    } else {
      ok('Free sample memory',
        `${fmtWords(plan.uploadWords)} words needed · ${fmtWords(freeWords - plan.uploadWords)} free after upload`);
    }
  }

  // ---- Estimate ----
  // Same envelope math as the slicing preflight: 122 wire bytes per
  // 60-word block, ~1.5 KB for the program, at baud/10 bytes per
  // second (8N1 + margin).
  const bytes = Math.ceil(plan.uploadWords / 60) * 122 + 1500;
  plan.estSeconds = Math.max(1, Math.round(bytes / Math.max(1, baud / 10)));

  return plan;
}

function pad(n: number): string {
  return n.toString().padStart(2, '0');
}

function fmtWords(n: number): string {
  return n.toLocaleString();
}
