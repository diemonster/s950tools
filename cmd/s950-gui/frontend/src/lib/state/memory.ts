// Memory-usage accounting for the connected S950. The device's SysEx
// surface has no "free memory" query, so we compute usage client-side
// by summing TotalWords across every SPRM in the catalog. Scans run
// automatically on device-touching user actions (Connect, Get from
// S950, post-ApplySlicing) so the displayed number reflects whatever
// state the user is about to act against.
//
// Total memory is configurable since the S950 has two flavours:
//   • base unit       : 512 kwords (≈750 KB at 1.5 bytes/word)
//   • +1 expansion    : 1024 kwords
//   • +2 (max EXM005) : 1536 kwords (≈2.25 MB)
// We can't auto-detect which the user has — defaults to base; the
// expansion flag is a localStorage preference toggled from the UI.
// Values match the device's boot-splash "K WORDS" readout, where
// K=1000 (not 1024).
//
// Local-only samples (drag-imported, Commit-Slices children) DON'T
// count toward used memory — they live in host memory only until an
// Apply / Send action pushes them to the device. The Apply preflight
// is the right place to predict post-upload usage.

import { writable, derived, get } from 'svelte/store';
import { samples } from './samples';
import * as App from '../../../wailsjs/go/main/App';
import { phase } from './connection';
import { sampleParamsToSample } from './converters';

// S950 memory ceilings in 12-bit words. K=1000 to match the device's
// boot-splash readout ("512 K WORDS" / "1536 K WORDS").
export const TOTAL_BASE   = 512000;
export const TOTAL_EXM005 = 1536000;

const KEY_EXM005 = 's950-tools.exm005';

function readExpansion(): boolean {
  if (typeof localStorage === 'undefined') return false;
  return localStorage.getItem(KEY_EXM005) === '1';
}

export const expansionEnabled = writable<boolean>(readExpansion());
expansionEnabled.subscribe((v) => {
  if (typeof localStorage !== 'undefined') {
    localStorage.setItem(KEY_EXM005, v ? '1' : '0');
  }
});

export const totalWords = derived(expansionEnabled, (v) => (v ? TOTAL_EXM005 : TOTAL_BASE));

export type MemoryState = {
  usedWords:   number;        // sum of TotalWords across all device samples
  sampleCount: number;        // how many samples contributed
  scanning:    boolean;       // a scan is in flight
  scannedAt:   number | null; // wall-clock ms of last successful scan
};

export const memoryUsage = writable<MemoryState>({
  usedWords: 0,
  sampleCount: 0,
  scanning: false,
  scannedAt: null,
});

// scanMemory walks every device-sourced sample, ensures its SPRM has
// been fetched, and sums TotalWords. Idempotent — calling repeatedly
// during a session re-uses any already-loaded SPRMs, so subsequent
// scans are near-instant after the first.
//
// Throws if the link drops mid-scan (caller's responsibility to
// reflect that to the UI). The store still flips scanning=false.
export async function scanMemory(): Promise<void> {
  // No point scanning without an open transport — the SPRM fetches
  // would all NAK. Cheap guard.
  if (get(phase) !== 'connected') return;
  memoryUsage.update((s) => ({ ...s, scanning: true }));
  try {
    const list = get(samples).filter((s) => s.source === 'device');
    for (const s of list) {
      // Use the same SPRM → Sample converter catalog.ts uses on
      // explicit selection so the scan populates every field (name,
      // rate, replay mode, loop config, …) rather than just length.
      // Without this, a fresh-connect screenshot displays the
      // skinnySample defaults for rate/etc. and only TotalWords
      // reflects the wire data — confusing during hardware testing.
      try {
        const params = await App.GetSampleParams(s.slot);
        const full = sampleParamsToSample(params, s.slot);
        // Merge instead of replace: scanMemory only refreshes SPRM
        // metadata (name, rate, length, loop, …) — it has no reason
        // to invalidate host-side audio (pcm/words12) that another
        // path attached, e.g. the post-Apply capture in
        // continueApplySlicing. Replacing would force the waveform
        // back to the synthetic squiggle the moment scanMemory ran.
        samples.update((xs) => xs.map((x) =>
          x.slot === s.slot ? { ...full, pcm: x.pcm, words12: x.words12 } : x,
        ));
      } catch {
        // Skip individual failures — one bad slot shouldn't abort
        // the whole scan. The corresponding sample stays at whatever
        // it had before (typically the skinny entry).
      }
    }
    // Recompute against the up-to-date samples store.
    const used = get(samples)
      .filter((s) => s.source === 'device')
      .reduce((sum, s) => sum + (s.length || 0), 0);
    memoryUsage.set({
      usedWords:   used,
      sampleCount: list.length,
      scanning:    false,
      scannedAt:   Date.now(),
    });
  } catch (e) {
    memoryUsage.update((s) => ({ ...s, scanning: false }));
    throw e;
  }
}

// Reset on disconnect so the chip clears instead of holding stale
// numbers from the previous session.
export function clearMemory() {
  memoryUsage.set({ usedWords: 0, sampleCount: 0, scanning: false, scannedAt: null });
}
