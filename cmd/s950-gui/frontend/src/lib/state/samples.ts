// Sample bank + cross-tab "selected sample" store. Shared between
// Keygroup (drag onto zone) and Sample (waveform editor).
//
// The list starts empty: there are no fictional samples anywhere in
// the app. Entries enter through one of two paths:
//   • catalog.refreshCatalog() on Connect — source: 'device'
//   • ImportSample() / drag-and-drop — source: 'local'
// The source flag drives the per-row sync badge so the user can tell
// at a glance which samples are actually on the S950 and which are
// imported but un-uploaded.

import { writable, derived, get } from 'svelte/store';

export type Sample = {
  slot: number;
  name: string;
  rate: number;      // Hz, e.g. 26040
  length: number;    // total words
  start: number;     // first replay point (words)
  end: number;       // end point (words)
  loopStart: number; // loop start (words)
  loopLength: number;// loop length (words)
  mode: 'one-shot' | 'loop' | 'ping-pong';
  reverse: boolean;
  velXfade: boolean;
  tune: number;      // semitones + cents
  loudness: number;  // signed
  // Sync state. 'device' means the row originated from the S950's
  // catalog (or was last successfully uploaded). 'local' means it
  // only exists in this session — drag-imported audio that hasn't
  // been uploaded yet. Drives the sidebar badge + identity-strip
  // text so the user can never mistake a local-only file for a
  // committed device slot.
  source: 'device' | 'local';
  // Immutable origin: set once on first creation, never mutated.
  // Lets the UI distinguish "purely local import" from "was on the
  // device, then edited" — the latter is revertable (click the
  // sidebar chip → re-pull SPRM from device, drop host PCM). The
  // existing `source` field collapses both cases to 'local' on
  // edit, which is why we need a separate immutable marker.
  originalSource?: 'device' | 'local';
  // Pre-edit snapshot of the device-coherent state. Captured on
  // the first user edit to a sample that was originally synced
  // with the device, so revert can restore the exact pre-edit
  // PCM + SPRM fields (we can't re-fetch from the device because
  // live-sync may have pushed intermediate writes to it). Cleared
  // on revert or successful Send-to-S950. SampleSnapshot omits
  // deviceSnapshot itself to avoid recursive nesting.
  deviceSnapshot?: SampleSnapshot;
  // Set on samples that were Commit-Slices'd from a parent source.
  // Lets the slicing UI re-lock the Commit button until the children
  // have been uploaded (source: 'device') — prevents double-committing
  // the same source mid-workflow. Cleared on Apply success.
  parentSlot?: number;
  // Round-trip cache of the original 120-byte SPRM block. Populated
  // by GetSampleParams and passed back on SetSampleParams so the
  // S950's reserved/undocumented bytes survive an edit cycle.
  raw?: number[];
  // ---------- Host-side audio (import-only) ----------
  // pcm: int16 mono PCM at `rate`, used for Web Audio preview.
  // words12: same audio as 12-bit S950 words, used as the slicing
  // source so we don't re-convert on Apply. Both arrays are
  // populated by ImportSample and never touched once stored.
  pcm?: number[];
  words12?: number[];
};

// SampleSnapshot is the captured pre-edit state — same shape as
// Sample minus the snapshot field. Used by revert to restore
// exactly what was on the device before the user touched anything.
export type SampleSnapshot = Omit<Sample, 'deviceSnapshot'>;

// Defaults used by both the import path and any future "add empty
// sample row" affordance. Centralised here so callers don't reinvent
// the field set every time.
export function newLocalSample(slot: number, name = '', rate = 26040, length = 0): Sample {
  return {
    slot,
    name,
    rate,
    length,
    start: 0,
    end: length,
    loopStart: 0,
    loopLength: 0,
    mode: 'one-shot',
    reverse: false,
    velXfade: false,
    tune: 0,
    loudness: 0,
    source: 'local',
    originalSource: 'local',
  };
}

// pickFreeSlot returns the lowest non-negative slot index that isn't
// in `list`. Used when an import lands without an explicit target —
// e.g. drag-and-drop while the sidebar is empty. Caps at 99 (the
// S950's slot ceiling); returns -1 if all 100 are taken.
export function pickFreeSlot(list: Sample[]): number {
  const taken = new Set(list.map((s) => s.slot));
  for (let i = 0; i < 100; i++) {
    if (!taken.has(i)) return i;
  }
  return -1;
}

// removeLocalSample drops a row from the samples store. Refuses to
// touch source: 'device' rows because the S950 has no remote-delete
// SysEx opcode — dropping it from the sidebar would silently
// disagree with the device's catalog. Returns true if the row was
// removed, false otherwise.
//
// If the deleted slot was the active selection, advances to the
// next still-present row (or the previous one, or unselected if the
// list ends up empty).
export function removeLocalSample(slot: number): boolean {
  const list = get(samples);
  const idx = list.findIndex((s) => s.slot === slot);
  if (idx < 0) return false;
  if (list[idx].source !== 'local') return false;

  const remaining = list.filter((_, i) => i !== idx);
  samples.set(remaining);
  if (get(selectedSampleSlot) === slot) {
    if (remaining.length === 0) {
      selectedSampleSlot.set(0);
    } else {
      const next = remaining[Math.min(idx, remaining.length - 1)];
      selectedSampleSlot.set(next.slot);
    }
  }
  return true;
}

export const samples = writable<Sample[]>([]);
// Default to slot 0 so the sidebar selection survives between an
// empty start and the first import.
export const selectedSampleSlot = writable<number>(0);

function makeSelectedSample() {
  const inner = derived(
    [samples, selectedSampleSlot],
    ([$samples, $selectedSampleSlot]) =>
      $samples.find((s) => s.slot === $selectedSampleSlot) ?? $samples[0]
  );
  return {
    subscribe: inner.subscribe,
    update(patch: Partial<Sample>) {
      const slot = get(selectedSampleSlot);
      samples.update((list) =>
        list.map((x) => {
          if (x.slot !== slot) return x;
          // Capture the device-coherent pre-edit state on the FIRST
          // user edit so revert can restore it byte-identically.
          // Why not re-fetch from the device on revert? Because
          // live-sync may have already pushed earlier edits to the
          // device's SPRM — the device no longer has the original
          // state to fetch. Snapshot here is the only source of
          // truth for "what was synced before the user touched it".
          // Only capture for samples that originated from the
          // device (originalSource === 'device') AND are currently
          // still synced (source === 'device'); purely-local rows
          // have no device counterpart to revert to.
          //
          // Caller can explicitly clear by passing
          // `deviceSnapshot: undefined` in patch — that override
          // takes precedence (used by Send-to-S950 success +
          // revert). The `'deviceSnapshot' in patch` check makes
          // the explicit-clear path safe; we only auto-set when
          // the caller didn't opine.
          let snapshot = x.deviceSnapshot;
          if (snapshot === undefined
              && !('deviceSnapshot' in patch)
              && x.source === 'device'
              && x.originalSource === 'device') {
            // Strip deviceSnapshot from the captured copy — the
            // SampleSnapshot type omits it to avoid recursive
            // nesting on subsequent edits.
            const { deviceSnapshot: _ignore, ...rest } = x;
            snapshot = rest;
          }
          const merged: Sample = { ...x, ...patch };
          if (!('deviceSnapshot' in patch)) {
            merged.deviceSnapshot = snapshot;
          }
          return merged;
        })
      );
      // Schedule SPRM write-back. Lazy-imported to avoid the
      // livesync ↔ samples circular import.
      void import('./livesync').then((m) => m.scheduleSampleWriteback(slot));
    },
  };
}

export const selectedSample = makeSelectedSample();
