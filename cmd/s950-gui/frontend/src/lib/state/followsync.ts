// Follow-selection sync: selecting a DEVICE program in the app sends
// the matching MIDI Program Change so the hardware follows the app —
// the standard MIDI-librarian pattern, made honest for a sampler
// that can't report its active program (no read primitive exists;
// see project notes).
//
// The activation only fires when it's PROVABLY unambiguous:
//   • the program lives on the device (source 'device'),
//   • RESPOND TO PC is enabled on it,
//   • its MIDI program number is UNIQUE among device programs
//     (PC selects by number, not slot — firing an ambiguous number
//     could activate a different program than the one clicked),
//   • a transport is connected.
// When a gate fails we do nothing and expose the reason, so the
// Keygroup grid can label its context truthfully instead of
// claiming a sync that may not exist. Feedback is always action
// language ("sent PC n") — never a "synced" state, which the
// hardware gives us no way to verify.

import { writable, get } from 'svelte/store';
import { programs, type Program } from './programs';
import { phase, status } from './connection';
import { ensureProgramLoaded, isProgramHydrated } from './catalog';
import * as App from '../../../wailsjs/go/main/App';

const PREF_KEY = 's950-tools.followProgram';

function readPref(): boolean {
  if (typeof localStorage === 'undefined') return true;
  return localStorage.getItem(PREF_KEY) !== '0';
}

// followEnabled is the persisted user toggle (default on).
export const followEnabled = writable<boolean>(readPref());
followEnabled.subscribe((v) => {
  if (typeof localStorage !== 'undefined') {
    localStorage.setItem(PREF_KEY, v ? '1' : '0');
  }
});

// lastActivation records the most recent SUCCESSFUL program-change
// send, so UI can say "→ sent PC n (NAME)" and the Keygroup grid can
// distinguish "we just pointed the hardware here" from "no idea
// what the hardware is playing". channel is the MIDI channel the PC
// went out on (0-based) — surfaced in tooltips so a basic-channel
// mismatch on the device is diagnosable instead of silent.
export type Activation = { slot: number; midiProg: number; name: string; channel: number };
export const lastActivation = writable<Activation | null>(null);

// followBlockedReason mirrors the most recent gate failure for the
// UI hints ('' when the last selection activated cleanly or follow
// never ran).
export const followBlockedReason = writable<string>('');

// resetFollowState wipes the activation claim + blocked reason.
// Called on catalog refresh (rows re-stubbed — the claim's slot may
// hold a different program now) and on connection loss below.
export function resetFollowState(): void {
  lastActivation.set(null);
  followBlockedReason.set('');
}

// A "→ PC sent" claim only survives while the connection that
// carried it lives; a reconnect may face a different S950 entirely.
// Entering 'connected' also clears any stale pre-connect blocked
// reason (e.g. 'not connected' from a click while offline).
phase.subscribe((p) => {
  if (p === 'connected') followBlockedReason.set('');
  else resetFollowState();
});

// invalidateActivation drops the activation claim for one slot.
// Called when the user edits the fields the claim depends on (MIDI
// prog # / RESPOND TO PC) — after a renumber, "sent PC n" no longer
// describes how to reach this program.
export function invalidateActivation(slot: number): void {
  lastActivation.update((a) => (a?.slot === slot ? null : a));
}

export type FollowDecision =
  | { ok: true; midiProg: number }
  | { ok: false; reason: string };

// decideFollow is the pure gate — exported for tests. `hydrated`
// reports whether a device program's REAL header has been read; rows
// that haven't carry fabricated stub defaults (midiProg 1, respondPC
// true), and trusting those could fire a PC that selects a different
// program than the one on screen. Hardware context for the
// uniqueness gate: the S950 selects EVERY responding program whose
// prog # matches an incoming PC (its layering feature), so a shared
// number can't ever mean "the one the user clicked".
export function decideFollow(
  prog: Program | undefined,
  allPrograms: Program[],
  connected: boolean,
  enabled: boolean,
  hydrated: (slot: number) => boolean,
): FollowDecision {
  if (!enabled) return { ok: false, reason: 'follow is off' };
  if (!connected) return { ok: false, reason: 'not connected' };
  if (!prog) return { ok: false, reason: 'no program selected' };
  if (prog.source !== 'device') {
    return { ok: false, reason: 'program is local-only — Send it to the S950 first' };
  }
  if (!hydrated(prog.slot)) {
    return { ok: false, reason: 'still reading this program from the S950 — select it again in a moment' };
  }
  if (!prog.respondPC) {
    return { ok: false, reason: `${prog.name || 'program'} has RESPOND TO PC disabled in its program header — enable it (and pick a unique MIDI PROG #) to use follow` };
  }
  const others = allPrograms.filter((p) => p.source === 'device' && p.slot !== prog.slot);
  if (others.some((p) => !hydrated(p.slot))) {
    return { ok: false, reason: 'still reading program headers from the S950 — uniqueness can\'t be checked yet' };
  }
  // Only respondPC programs can react to the PC, so a deaf program
  // sharing the number doesn't make it ambiguous.
  const sharers = others.filter((p) => p.respondPC && p.midiProg === prog.midiProg);
  if (sharers.length > 0) {
    return {
      ok: false,
      reason: `${sharers.length + 1} device programs share MIDI prog #${prog.midiProg} — the S950 selects ALL of them on that PC (layering), so follow can't target one. Give each program a unique MIDI PROG #.`,
    };
  }
  return { ok: true, midiProg: prog.midiProg };
}

let debounceTimer: ReturnType<typeof setTimeout> | null = null;
const DEBOUNCE_MS = 400;

// generation guards the awaited stretch of activateIfUnambiguous:
// a newer click invalidates everything an older in-flight activation
// would have concluded, so a slow header fetch or wire send can't
// stamp lastActivation for a program the user has already left.
let generation = 0;

// onProgramSelected is called when the USER picks a program — wire
// it from click sites only, never from programmatic selectedSlot
// writes (refresh migration, New/Duplicate/Open, send slot-swaps
// must not push Program Changes at the hardware). Re-clicking the
// already-selected program deliberately re-sends its PC.
export function onProgramSelected(slot: number): void {
  const myGen = ++generation;
  if (debounceTimer) clearTimeout(debounceTimer);
  debounceTimer = setTimeout(() => {
    void activateIfUnambiguous(slot, myGen);
  }, DEBOUNCE_MS);
}

async function activateIfUnambiguous(slot: number, myGen: number): Promise<void> {
  // Make sure the selected program's REAL header is in the store
  // before deciding — the 400ms debounce can beat the lazy GetProgram
  // fetch the selection also triggered, and deciding on the stub's
  // fabricated midiProg would send a PC for the wrong number.
  // ensureProgramLoaded is a no-op when already hydrated; errors fall
  // through to the hydrated() gate below, which blocks honestly.
  try {
    await ensureProgramLoaded(slot);
  } catch {
    // logged inside ensureProgramLoaded
  }
  if (myGen !== generation) return; // superseded by a newer click
  const all = get(programs);
  const prog = all.find((p) => p.slot === slot);
  const decision = decideFollow(
    prog, all, get(phase) === 'connected', get(followEnabled), isProgramHydrated,
  );
  if (!decision.ok) {
    followBlockedReason.set(decision.reason);
    // A blocked selection displaces a matching earlier success —
    // otherwise the UI keeps flashing the green "PC sent" badge for
    // a slot whose latest selection did NOT reach the hardware.
    invalidateActivation(slot);
    return;
  }
  try {
    const channel = get(status).channel ?? 0;
    await (App as any).ActivateProgram(decision.midiProg, channel);
    if (myGen !== generation) return; // a newer click owns the UI now
    lastActivation.set({ slot, midiProg: decision.midiProg, name: prog!.name, channel });
    followBlockedReason.set('');
  } catch (e: any) {
    if (myGen !== generation) return;
    followBlockedReason.set('program change failed: ' + String(e?.message ?? e));
    invalidateActivation(slot);
  }
}
