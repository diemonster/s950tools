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

import { writable } from 'svelte/store';
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

let eventsWired = false;

// initMidiThruEvents subscribes to backend state pushes (throttled
// counter updates while forwarding). Idempotent.
export function initMidiThruEvents(
  eventsOn: (name: string, cb: (data: MidiThruState) => void) => void,
): void {
  if (eventsWired) return;
  eventsWired = true;
  eventsOn('midithru:state', (st) => {
    midiThru.set(st ?? { ...INACTIVE });
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
// serial connect. Best-effort: a failure (e.g. virtual ports
// unsupported on this platform, remembered input unplugged) lands in
// midiThruError for the chip tooltip but never blocks the session.
export async function autoStartThru(): Promise<void> {
  const pref = thruPref();
  if (pref === '') return;
  if (pref === 'virtual') {
    await startThru('', true);
  } else if (pref.startsWith('port:')) {
    await startThru(pref.slice('port:'.length), false);
  }
}
