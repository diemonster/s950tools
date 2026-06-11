// MIDI-thru state — the topbar THRU chip's backing store. Forwards
// live MIDI (from a virtual port the DAW targets, or a physical
// MIDI-in) down the RS-232 wire, because the S950 ignores its DIN
// jacks entirely while controller-select is on RS-232C
// (hardware-verified).
//
// The store mirrors the backend's "midithru:state" events; start/
// stop call the Wails bindings. Event subscription is wired by
// initMidiThruEvents() — called once from Topbar's onMount rather
// than at module load so tests can import this module without a
// Wails runtime present.

import { writable, get } from 'svelte/store';
import * as App from '../../../wailsjs/go/main/App';

export type MidiThruState = {
  active: boolean;
  source: string;
  virtual: boolean;
  forwarded: number;
  dropped: number;
};

const INACTIVE: MidiThruState = {
  active: false,
  source: '',
  virtual: false,
  forwarded: 0,
  dropped: 0,
};

export const midiThru = writable<MidiThruState>({ ...INACTIVE });

// Last start/stop error for the chip's tooltip; cleared on success.
export const midiThruError = writable<string>('');

// Platform capabilities — shapes the THRU chip. Windows has no
// app-created MIDI ports (WinMM), so the virtual option is hidden
// there and the hint steers users to a loopback driver (loopMIDI)
// selected as a regular input. Defaults assume virtual support so
// macOS/Linux render correctly even before the binding resolves.
export type MidiThruCaps = {
  virtualSupported: boolean;
  virtualPortName: string;
  hint: string;
};

export const thruCaps = writable<MidiThruCaps>({
  virtualSupported: true,
  virtualPortName: 'S950 RS-232 (s950-tools)',
  hint: '',
});

export async function loadThruCaps(): Promise<void> {
  try {
    const caps = await (App as any).MidiThruCaps();
    if (caps) thruCaps.set(caps);
  } catch {
    // Binding unreachable (test render) — keep optimistic defaults.
  }
}

// ---------- Live note visualisation ----------
// One event per FORWARDED note (the sampler's actual input). The
// keygroup grid renders these Renoise-style: held notes light the
// keyboard, and each hit drops a fading blip at its (note, velocity)
// point on the zone canvas — which is already note×velocity space.

export type ThruNoteEvent = {
  note: number;
  channel: number;
  velocity: number;
  on: boolean;
};

export type ThruBlip = { id: number; note: number; velocity: number };

// thruHeld: note → velocity for currently-sounding notes.
export const thruHeld = writable<Map<number, number>>(new Map());
// thruBlips: recent note-on hits; each self-removes after its fade.
export const thruBlips = writable<ThruBlip[]>([]);

const BLIP_LIFETIME_MS = 900;
const BLIP_CAP = 48; // burst guard — oldest drop first
let blipSeq = 0;

function onThruNote(ev: ThruNoteEvent): void {
  if (ev.on) {
    thruHeld.update((m) => {
      const next = new Map(m);
      next.set(ev.note, ev.velocity);
      return next;
    });
    const blip: ThruBlip = { id: ++blipSeq, note: ev.note, velocity: ev.velocity };
    thruBlips.update((bs) => {
      const next = [...bs, blip];
      return next.length > BLIP_CAP ? next.slice(next.length - BLIP_CAP) : next;
    });
    setTimeout(() => {
      thruBlips.update((bs) => bs.filter((b) => b.id !== blip.id));
    }, BLIP_LIFETIME_MS);
  } else {
    thruHeld.update((m) => {
      if (!m.has(ev.note)) return m;
      const next = new Map(m);
      next.delete(ev.note);
      return next;
    });
  }
}

function clearThruNotes(): void {
  thruHeld.set(new Map());
  thruBlips.set([]);
}

let eventsWired = false;

// initMidiThruEvents subscribes to backend state pushes (throttled
// counter updates while forwarding) and per-note visualisation
// events. Idempotent.
export function initMidiThruEvents(
  eventsOn: (name: string, cb: (data: any) => void) => void,
): void {
  if (eventsWired) return;
  eventsWired = true;
  eventsOn('midithru:state', (st: MidiThruState) => {
    midiThru.set(st ?? { ...INACTIVE });
    // Thru stopped → nothing is sounding anymore; clear the lights
    // so a key can't stay lit across sessions.
    if (!st?.active) clearThruNotes();
  });
  eventsOn('midithru:note', (ev: ThruNoteEvent) => {
    if (ev) onThruNote(ev);
  });
}

// startThru begins forwarding. virtual=true publishes the app's
// virtual MIDI destination (shows up in the DAW's output list);
// otherwise `port` names an existing MIDI input to tap.
export async function startThru(port: string, virtual: boolean): Promise<boolean> {
  midiThruError.set('');
  try {
    const st = await (App as any).MidiThruStart(port, virtual);
    if (st) midiThru.set(st);
    return true;
  } catch (e: any) {
    midiThruError.set(String(e?.message ?? e));
    return false;
  }
}

export async function stopThru(): Promise<void> {
  midiThruError.set('');
  try {
    await (App as any).MidiThruStop();
  } catch (e: any) {
    midiThruError.set(String(e?.message ?? e));
  }
  midiThru.set({ ...INACTIVE });
}

// syncThruStatus pulls the backend's current thru state into the
// store. Called on Topbar mount: the backend keeps forwarding across
// webview/dev reloads while this module's store re-initialises to
// INACTIVE — without the resync the chip would show "off" while
// notes still flow, with no way to stop them from the UI.
export async function syncThruStatus(): Promise<void> {
  try {
    const st = await (App as any).MidiThruStatus();
    if (st) midiThru.set(st);
  } catch {
    // Backend unreachable (e.g. test render) — keep the inactive
    // default.
  }
}

// ---------- Auto-start preference ----------
// Thru defaults ON (virtual port) for serial sessions so the DAW
// workflow is zero-config: connect over RS-232 and "S950 RS-232
// (s950-tools)" is immediately targetable from Ableton. The user's
// chip choice persists across sessions: picking "off" stays off on
// the next connect; picking a physical input restores that input.
//
// Stored values: 'virtual' | 'port:<name>' | '' (off).
const PREF_KEY = 's950-tools.midithru';

export function thruPref(): string {
  if (typeof localStorage === 'undefined') return 'virtual';
  const v = localStorage.getItem(PREF_KEY);
  return v === null ? 'virtual' : v;
}

export function setThruPref(v: string): void {
  if (typeof localStorage === 'undefined') return;
  localStorage.setItem(PREF_KEY, v);
}

// autoStartThru applies the persisted preference after a successful
// serial connect. Best-effort: a failure (e.g. remembered input
// unplugged) lands in midiThruError for the chip tooltip but never
// blocks the session. The 'virtual' default is skipped silently on
// platforms without virtual-port support (Windows) — there the chip
// shows the loopMIDI hint instead of a spurious startup error.
export async function autoStartThru(): Promise<void> {
  const pref = thruPref();
  if (pref === '') return;
  if (pref === 'virtual') {
    await loadThruCaps();
    if (!get(thruCaps).virtualSupported) return;
    await startThru('', true);
  } else if (pref.startsWith('port:')) {
    await startThru(pref.slice('port:'.length), false);
  }
}
