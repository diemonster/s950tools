// Connection state — mirrors the Wails-side App.Connect lifecycle.
// The topbar reads this store to render port dropdowns + the
// connect/disconnect button; the sync chip's phase flips through
// these states so the user always sees whether the device is live.

import { writable, get } from 'svelte/store';
import * as App from '../../../wailsjs/go/main/App';
import { refreshCatalog, ensureProgramLoaded } from './catalog';
import { scanMemory, clearMemory } from './memory';
import { selectedSlot as selectedProgramSlot } from './programs';

export type Port = { name: string };
export type Status = { connected: boolean; in?: string; out?: string; channel: number };

// Link phase is *connection* state — separate from the per-tab edit
// "sync" state (synced / dirty / sending). Both can be live at once:
// e.g. connected + dirty means the device link is fine but local
// edits haven't been Sent yet.
export type LinkPhase = 'unknown' | 'disconnected' | 'connecting' | 'connected' | 'error';

// ---------- Persistence ----------
// MIDI picks survive app restarts via localStorage so the user
// isn't forced to repick the same interface on every launch. Same
// pattern as theme.ts: read on init, mirror on every change.
// If the saved port name doesn't exist anymore (user unplugged a
// device), the select shows blank — better than auto-connecting to
// the wrong interface.
const KEY_IN      = 's950-tools.midi-in';
const KEY_OUT     = 's950-tools.midi-out';
const KEY_CHANNEL = 's950-tools.midi-channel';

function readString(key: string): string {
  if (typeof localStorage === 'undefined') return '';
  return localStorage.getItem(key) ?? '';
}
function readChannel(): number {
  if (typeof localStorage === 'undefined') return 0;
  const raw = parseInt(localStorage.getItem(KEY_CHANNEL) ?? '', 10);
  return Number.isInteger(raw) && raw >= 0 && raw <= 15 ? raw : 0;
}
function writeKey(key: string, value: string) {
  if (typeof localStorage === 'undefined') return;
  localStorage.setItem(key, value);
}

export const inputPorts  = writable<Port[]>([]);
export const outputPorts = writable<Port[]>([]);
// User's current picks. Empty string until they choose; if empty on
// Connect, the backend's `transport.Open` substring-matches against
// the first available port.
export const selectedIn  = writable<string>(readString(KEY_IN));
export const selectedOut = writable<string>(readString(KEY_OUT));
export const channel     = writable<number>(readChannel());
export const phase       = writable<LinkPhase>('unknown');
export const status      = writable<Status>({ connected: false, channel: 0 });
export const linkError   = writable<string>('');

// Mirror every change back to localStorage. Subscribers run
// synchronously, including the initial fire on subscribe — that's
// fine, it just re-writes the value we just read.
selectedIn.subscribe ((v) => writeKey(KEY_IN,      v));
selectedOut.subscribe((v) => writeKey(KEY_OUT,     v));
channel.subscribe    ((v) => writeKey(KEY_CHANNEL, String(v)));

// refreshPorts enumerates available MIDI in/out ports from the
// backend (which wraps rtmidi). Called on app mount and after a
// successful connect so the dropdowns always reflect what's plugged in.
export async function refreshPorts() {
  try {
    const list = await App.ListPorts();
    inputPorts.set(list.ins ?? []);
    outputPorts.set(list.outs ?? []);
    linkError.set('');
  } catch (e: any) {
    linkError.set(String(e?.message ?? e));
  }
}

// connect opens the transport with the currently-selected ports
// + channel. Empty port strings let the backend pick the first
// available port (substring match).
export async function connect() {
  phase.set('connecting');
  linkError.set('');
  try {
    await App.Connect(get(selectedIn), get(selectedOut), get(channel));
    await refreshStatus();
    // Pull the device catalog immediately so the sidebars reflect
    // what's actually on the S950, not the dev-time stubs. Failures
    // here don't break the connection — surface as a soft error so
    // the user can retry without disconnecting.
    if (get(phase) === 'connected') {
      try {
        await refreshCatalog();
      } catch (e: any) {
        linkError.set('Catalog fetch failed: ' + String(e?.message ?? e));
      }
      // Memory scan kicks off in the background — Connect returns
      // as soon as the catalog lands so the user can start clicking
      // around; the topbar memory chip flips to "Scanning…" until
      // the per-sample SPRM fetches complete.
      void scanMemory();
      // Eagerly load the currently-selected program's full PRGM
      // payload too. Samples are covered by scanMemory above; the
      // program-tab auto-loader (selectedSlot.subscribe in
      // catalog.ts) only fires on store *changes*, so a fresh
      // Connect where the slot was already 0 wouldn't otherwise
      // pull the keygroups until the user manually clicked Get.
      // Other programs stay lazy — loaded when the user navigates
      // to them (cheap on selection change).
      void ensureProgramLoaded(get(selectedProgramSlot));
    }
  } catch (e: any) {
    phase.set('error');
    linkError.set(String(e?.message ?? e));
  }
}

export async function disconnect() {
  try {
    await App.Disconnect();
  } catch (e: any) {
    // Ignore — we're closing anyway. Surfaced just in case.
    linkError.set(String(e?.message ?? e));
  }
  phase.set('disconnected');
  status.set({ connected: false, channel: 0 });
  // Clear the memory chip so it doesn't show stale numbers from the
  // previous session after the user disconnects + reconnects to a
  // different device.
  clearMemory();
}

// refreshStatus asks the backend whether the transport is still
// open + reads back the resolved port names (the backend may have
// picked "MRCC Port 03" when the user gave empty/substring match).
export async function refreshStatus() {
  try {
    const s = await App.Status();
    status.set(s);
    phase.set(s.connected ? 'connected' : 'disconnected');
    if (s.connected) {
      if (s.in)  selectedIn.set(s.in);
      if (s.out) selectedOut.set(s.out);
      channel.set(s.channel);
    }
  } catch (e: any) {
    linkError.set(String(e?.message ?? e));
  }
}
