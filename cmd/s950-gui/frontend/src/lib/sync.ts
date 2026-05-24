// Sync status — drives the topbar badge. Each page sets the state
// that's relevant to it (Program is dirty-until-Send, Keygroup
// live-syncs at ~400ms, Sample mostly live-syncs except for audio
// data uploads which are blocking).

import { writable } from 'svelte/store';

export type SyncState = 'synced' | 'dirty' | 'sending' | 'error';

export interface SyncStatus {
  state: SyncState;
  label: string;
}

export const sync = writable<SyncStatus>({ state: 'synced', label: 'Synced' });

export function setSync(state: SyncState, label?: string) {
  sync.set({ state, label: label ?? defaultLabel(state) });
}

function defaultLabel(state: SyncState): string {
  switch (state) {
    case 'synced':  return 'Synced';
    case 'dirty':   return 'Unsaved';
    case 'sending': return 'Sending…';
    case 'error':   return 'Sync error';
  }
}
