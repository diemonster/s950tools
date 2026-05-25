// Connection state — mirrors the Wails-side App.Connect lifecycle.
// The topbar reads this store to render port dropdowns + the
// connect/disconnect button; the sync chip's phase flips through
// these states so the user always sees whether the device is live.

import { writable, get } from 'svelte/store';
import * as App from '../../../wailsjs/go/main/App';
import { refreshCatalog } from './catalog';

export type Port = { name: string };
export type Status = { connected: boolean; in?: string; out?: string; channel: number };

// Link phase is *connection* state — separate from the per-tab edit
// "sync" state (synced / dirty / sending). Both can be live at once:
// e.g. connected + dirty means the device link is fine but local
// edits haven't been Sent yet.
export type LinkPhase = 'unknown' | 'disconnected' | 'connecting' | 'connected' | 'error';

export const inputPorts  = writable<Port[]>([]);
export const outputPorts = writable<Port[]>([]);
// User's current picks. Empty string until they choose; if empty on
// Connect, the backend's `transport.Open` substring-matches against
// the first available port.
export const selectedIn  = writable<string>('');
export const selectedOut = writable<string>('');
export const channel     = writable<number>(0);
export const phase       = writable<LinkPhase>('unknown');
export const status      = writable<Status>({ connected: false, channel: 0 });
export const linkError   = writable<string>('');

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
