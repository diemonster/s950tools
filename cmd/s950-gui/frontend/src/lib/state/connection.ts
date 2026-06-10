// Connection state — mirrors the Wails-side App.Connect lifecycle.
// The topbar reads this store to render port dropdowns + the
// connect/disconnect button; the sync chip's phase flips through
// these states so the user always sees whether the device is live.

import { writable, get } from 'svelte/store';
import * as App from '../../../wailsjs/go/main/App';
import { refreshCatalog, ensureProgramLoaded } from './catalog';
import { scanMemory, clearMemory } from './memory';
import { selectedSlot as selectedProgramSlot } from './programs';
import { hydrateAllSamplesFromCache } from './waveformcache';

export type Port = { name: string };
export type SerialPort = { name: string };
// 'midi' | 'serial' — drives the dispatch in connect() and gates
// transport-specific features (Copy-from-S950 needs serial because
// MIDI's pre-emptive ACK pump can't handle large samples reliably).
export type TransportKind = 'midi' | 'serial';
export type Status = {
  connected: boolean;
  kind?: string;
  in?: string;
  out?: string;
  channel: number;
  baud?: number;
};

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
const KEY_KIND    = 's950-tools.transport-kind';
const KEY_BAUD    = 's950-tools.serial-baud';

// Practical baud rates the S950's UART can hit cleanly (verified on
// firmware 1.2a — see project-pending-work memory). 50000 is the
// effective ceiling: above that the device's clock divider can't
// generate a matching rate (76800 falls back to ACTUAL 71420, ~7%
// off, garbage). 38400 is the conservative default.
export const SERIAL_BAUDS = [9600, 19200, 38400, 50000] as const;
export type SerialBaud = (typeof SERIAL_BAUDS)[number];

function readString(key: string): string {
  if (typeof localStorage === 'undefined') return '';
  return localStorage.getItem(key) ?? '';
}
function readChannel(): number {
  if (typeof localStorage === 'undefined') return 0;
  const raw = parseInt(localStorage.getItem(KEY_CHANNEL) ?? '', 10);
  return Number.isInteger(raw) && raw >= 0 && raw <= 15 ? raw : 0;
}
function readKind(): TransportKind {
  if (typeof localStorage === 'undefined') return 'midi';
  return localStorage.getItem(KEY_KIND) === 'serial' ? 'serial' : 'midi';
}
function readBaud(): SerialBaud {
  if (typeof localStorage === 'undefined') return 38400;
  const raw = parseInt(localStorage.getItem(KEY_BAUD) ?? '', 10);
  return (SERIAL_BAUDS as readonly number[]).includes(raw) ? (raw as SerialBaud) : 38400;
}
function writeKey(key: string, value: string) {
  if (typeof localStorage === 'undefined') return;
  localStorage.setItem(key, value);
}

export const inputPorts  = writable<Port[]>([]);
export const outputPorts = writable<Port[]>([]);
export const serialPortsList = writable<SerialPort[]>([]);
// User's current picks. Empty string until they choose; if empty on
// Connect, the backend's `transport.Open` substring-matches against
// the first available port.
export const selectedIn  = writable<string>(readString(KEY_IN));
export const selectedOut = writable<string>(readString(KEY_OUT));
export const channel     = writable<number>(readChannel());
// transportKind controls which backend method connect() invokes.
// Flipped automatically when the user picks a serial port in either
// dropdown (so it stays in sync with what's actually selected),
// persisted so the next launch defaults to the same transport.
export const transportKind = writable<TransportKind>(readKind());
// baud is the RS-232 line rate. Only meaningful when
// transportKind === 'serial'. Ignored for MIDI sessions.
export const baud         = writable<SerialBaud>(readBaud());
export const phase        = writable<LinkPhase>('unknown');
export const status       = writable<Status>({ connected: false, channel: 0 });
export const linkError    = writable<string>('');
// serialUnresponsive flips true when connect() opens the serial port
// successfully but the post-connect ping times out — the silent
// failure mode where the cable is fine but the S950 still has
// controller-select on MIDI (or is off, or on a different baud).
// The topbar subscribes and renders an explanatory modal; the modal's
// Close button calls dismissSerialUnresponsive() to flip this back.
export const serialUnresponsive = writable<boolean>(false);
export function dismissSerialUnresponsive() {
  serialUnresponsive.set(false);
}

// Mirror every change back to localStorage. Subscribers run
// synchronously, including the initial fire on subscribe — that's
// fine, it just re-writes the value we just read.
selectedIn.subscribe   ((v) => writeKey(KEY_IN,      v));
selectedOut.subscribe  ((v) => writeKey(KEY_OUT,     v));
channel.subscribe      ((v) => writeKey(KEY_CHANNEL, String(v)));
transportKind.subscribe((v) => writeKey(KEY_KIND,    v));
baud.subscribe         ((v) => writeKey(KEY_BAUD,    String(v)));

// refreshPorts enumerates available MIDI in/out ports + RS-232
// serial devices from the backend. Called on app mount and after a
// successful connect so the dropdowns always reflect what's
// plugged in.
export async function refreshPorts() {
  try {
    const list = await App.ListPorts();
    inputPorts.set(list.ins ?? []);
    outputPorts.set(list.outs ?? []);
    serialPortsList.set(list.serial ?? []);
    linkError.set('');
  } catch (e: any) {
    linkError.set(String(e?.message ?? e));
  }
}

// pickSerial sets both MIDI in/out to a serial port name and flips
// transportKind to 'serial'. RS-232 is bidirectional on one cable,
// so picking the device for one direction implies the other. Called
// from the topbar when the user picks anything in the serial
// optgroup of either dropdown.
export function pickSerial(portName: string) {
  selectedIn.set(portName);
  selectedOut.set(portName);
  transportKind.set('serial');
}

// pickMidi sets one direction to a MIDI port name (the other stays
// untouched). Flips transportKind back to 'midi' if we'd previously
// been on serial. Called from the topbar when the user picks
// anything in the MIDI optgroup.
export function pickMidi(direction: 'in' | 'out', portName: string) {
  if (direction === 'in')  selectedIn.set(portName);
  if (direction === 'out') selectedOut.set(portName);
  // If switching back from serial, clear the mirrored port name on
  // the other side — it was a serial path, not a MIDI port.
  if (get(transportKind) === 'serial') {
    if (direction === 'in')  selectedOut.set('');
    if (direction === 'out') selectedIn.set('');
  }
  transportKind.set('midi');
}

// connect opens the transport with the currently-selected ports
// + channel. Dispatches MIDI vs serial based on transportKind so
// the topbar's chip-picker UI can stay agnostic of the backend
// method. Empty MIDI port strings let the backend pick the first
// available port (substring match).
export async function connect() {
  phase.set('connecting');
  linkError.set('');
  serialUnresponsive.set(false);
  const wasSerial = get(transportKind) === 'serial';
  try {
    if (wasSerial) {
      await App.ConnectSerial(get(selectedIn), get(baud));
    } else {
      await App.Connect(get(selectedIn), get(selectedOut), get(channel));
    }
    // Verify the sampler actually responds before declaring success.
    // The OS-level port open succeeds even if the S950 is still in
    // MIDI controller-select mode, off, or on a mismatched baud —
    // without this round-trip the chip would say "Connected" while
    // every subsequent operation timed out silently. On miss, tear
    // down the half-open transport so Status reflects reality and
    // pop an RS-232-specific modal (serial path only).
    try {
      await App.VerifyDevice();
    } catch (e: any) {
      try { await App.Disconnect(); } catch {}
      status.set({ connected: false, channel: 0 });
      if (wasSerial) {
        phase.set('disconnected');
        serialUnresponsive.set(true);
      } else {
        phase.set('error');
        linkError.set('Device is not responding: ' + String(e?.message ?? e));
      }
      return;
    }
    await refreshStatus();
    // Auto-start MIDI thru on serial sessions (defaults to the
    // virtual port so DAWs can target the S950 with zero setup; the
    // user's chip choice persists — "off" stays off). Fire-and-
    // forget: thru is a convenience layer, never a connect blocker.
    if (get(status).kind === 'serial') {
      void (async () => {
        try {
          const { autoStartThru } = await import('./midithru');
          await autoStartThru();
        } catch {}
      })();
    }
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
      // scanMemory + Phase 1B hydration run as a background chain.
      // Order matters: scanMemory refreshes each device sample's
      // SPRM-reported `length` field (replacing the skinny default
      // of 1 from refreshCatalog), and hydration's cache key
      // includes that length. Running them in parallel would have
      // hydrate looking up `(slot, name, 1)` and missing every
      // entry. Awaiting scanMemory first means hydrate sees the
      // real lengths and matches what persistSampleToCache wrote.
      // The chain is still fire-and-forget from connect() so the
      // UI can paint while the chip says "Scanning…".
      void (async () => {
        try { await scanMemory(); } catch {}
        try { await hydrateAllSamplesFromCache(); } catch {}
      })();
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
      if (s.kind === 'serial' || s.kind === 'midi') {
        transportKind.set(s.kind as TransportKind);
      }
      if (s.kind === 'serial' && s.baud) {
        baud.set(s.baud as SerialBaud);
      }
    }
  } catch (e: any) {
    linkError.set(String(e?.message ?? e));
  }
}
