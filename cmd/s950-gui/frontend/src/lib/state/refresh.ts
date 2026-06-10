// Full-device-state refresh — re-pulls catalog, sample params, and
// every program from the S950 in one go. User-initiated from the
// topbar (only visible on RS-232 since the round-trip count is
// punishing over MIDI).
//
// Distinct from Connect's auto-sync: Connect runs once per session
// and is "best effort, don't block the UI." This is the explicit
// "the device drifted from my view, fix it" button, so it owns a
// dedicated modal with per-step progress.

import { writable, get } from 'svelte/store';
import { refreshCatalog, ensureProgramLoaded } from './catalog';
import { scanMemory } from './memory';
import { programs } from './programs';
import { samples } from './samples';
import { wordsToPcm, persistSampleToCache } from './waveformcache';
import * as App from '../../../wailsjs/go/main/App';

export type RefreshPhase = 'idle' | 'catalog' | 'samples' | 'programs' | 'audio' | 'done' | 'error';

export type RefreshState = {
  phase: RefreshPhase;
  message: string;
  // Overall progress in 0..100. We split the budget: catalog +
  // samples = 25%, programs = 75% (programs are the bulk of the
  // wire time — 100 × PRGM is ~minutes even on RS-232).
  progress: number;
  // Sub-counters for the current step ("program 5/12"). Zero when
  // the phase doesn't have a per-item loop.
  subCurrent: number;
  subTotal: number;
  error?: string;
  // Set by cancelRefresh() — the runner checks this between
  // iterations of the program loop. We can't preempt a Wails call
  // in flight, so cancel is best-effort: the current call completes
  // before the loop exits.
  cancelled: boolean;
};

const INITIAL: RefreshState = {
  phase: 'idle',
  message: '',
  progress: 0,
  subCurrent: 0,
  subTotal: 0,
  cancelled: false,
};

export const refreshState = writable<RefreshState>({ ...INITIAL });

export function resetRefresh() {
  refreshState.set({ ...INITIAL });
}

export function cancelRefresh() {
  refreshState.update((r) => ({ ...r, cancelled: true }));
}

// refreshAllFromDevice walks the catalog and pulls fresh state for
// every program slot the device reports. Sample params are bulk-
// fetched by scanMemory in one pass; programs are fetched one-at-a-
// time so the progress bar can advance in real time. Failures on
// individual programs are logged and skipped — one bad slot
// shouldn't tank the whole sync.
//
// `withAudio` controls whether the audio phase runs (Phase 1C
// per-sample SDATA pulls). Default true: matches the documented
// modal copy. Setting false runs metadata-only — useful when the
// user just wants names/loop/program state and audio hasn't
// drifted (typical "I edited a program on the front panel" case).
export async function refreshAllFromDevice(withAudio = true): Promise<void> {
  refreshState.set({
    ...INITIAL,
    phase: 'catalog',
    message: 'Fetching catalog…',
    progress: 2,
  });

  try {
    await refreshCatalog();
    if (get(refreshState).cancelled) return finishCancelled();

    refreshState.update((r) => ({
      ...r,
      phase: 'samples',
      message: 'Scanning sample parameters…',
      progress: 10,
    }));
    await scanMemory();
    if (get(refreshState).cancelled) return finishCancelled();

    // refreshCatalog populates `programs` with skinny stubs; loop
    // through and force-pull each PRGM. ensureProgramLoaded is the
    // same code path Connect uses, so cached entries from before
    // this refresh get cleanly overwritten via force=true.
    const list = get(programs);
    const total = list.length;
    refreshState.update((r) => ({
      ...r,
      phase: 'programs',
      message: total === 0 ? 'No programs on device' : `Fetching program 1/${total}…`,
      subTotal: total,
      subCurrent: 0,
      progress: 25,
    }));

    // Programs cover 25% → 60% of the overall bar. Audio is the
    // longest phase by wall-clock so it gets the remaining 40%.
    for (let i = 0; i < total; i++) {
      if (get(refreshState).cancelled) return finishCancelled();
      const p = list[i];
      const label = p.name ? `${p.name} (slot ${p.slot})` : `slot ${p.slot}`;
      refreshState.update((r) => ({
        ...r,
        message: `Fetching program ${i + 1}/${total}: ${label}`,
        subCurrent: i + 1,
        progress: 25 + (i / Math.max(1, total)) * 35,
      }));
      try {
        await ensureProgramLoaded(p.slot, true);
      } catch (e) {
        // Log and continue — a single failed program shouldn't
        // abort the whole refresh. The user sees the final state
        // succeed for everything else; the failed slot keeps its
        // pre-refresh data (last known good).
        console.warn(`refresh: program ${p.slot} failed:`, e);
      }
    }

    // Cancel may have been set inside the LAST program's
    // ensureProgramLoaded — the loop's top-of-iteration check
    // doesn't fire after the final iteration, so re-check here
    // before we kick off the (potentially slow) audio phase.
    if (get(refreshState).cancelled) return finishCancelled();

    // User opted out of audio via the modal toggle. Skip straight
    // to done — metadata-only refresh is the fast path (seconds vs
    // minutes) and matches the "I edited programs on the front
    // panel" use case.
    if (!withAudio) {
      refreshState.set({
        ...INITIAL,
        phase: 'done',
        message: total === 0
          ? 'Refresh complete (no programs on device)'
          : `Refreshed ${total} program${total === 1 ? '' : 's'} + samples (audio skipped)`,
        progress: 100,
      });
      return;
    }

    // Audio pass: pull SDATA for every device sample that doesn't
    // already have host-side PCM. Phase 1B's disk cache means
    // subsequent refreshes skip everything (~instant) — only the
    // first refresh per session has to pay for the wire transfer.
    // Worst case: a fully-loaded EXM005 (~1.57M words) over 50000-
    // baud serial = ~10 min total; typical case is well under.
    const needsAudio = get(samples).filter((s) => s.source === 'device' && (!s.pcm || s.pcm.length === 0));
    refreshState.update((r) => ({
      ...r,
      phase: 'audio',
      message: needsAudio.length === 0
        ? 'All sample audio already cached'
        : `Copying audio (1/${needsAudio.length})…`,
      subTotal: needsAudio.length,
      subCurrent: 0,
      progress: 60,
    }));
    for (let i = 0; i < needsAudio.length; i++) {
      if (get(refreshState).cancelled) return finishCancelled();
      const s = needsAudio[i];
      const label = s.name ? `${s.name} (slot ${s.slot})` : `slot ${s.slot}`;
      refreshState.update((r) => ({
        ...r,
        message: `Copying audio ${i + 1}/${needsAudio.length}: ${label}`,
        subCurrent: i + 1,
        progress: 60 + (i / Math.max(1, needsAudio.length)) * 40,
      }));
      try {
        const copied = (await App.CopySampleAudio(s.slot)) as { words?: number[]; sampleRateHz?: number } | null;
        const words = copied?.words ?? [];
        if (!Array.isArray(words) || words.length === 0) continue;
        const pcm = wordsToPcm(words);
        // Override the SPRM rate with the dump header's rate — the
        // dump is authoritative for the audio bytes that just landed.
        // Falls back to the row's existing s.rate when the backend
        // didn't supply one (defensive against a mid-migration build).
        const rate = copied?.sampleRateHz ?? s.rate;
        samples.update((xs) => xs.map((x) =>
          x.slot === s.slot ? { ...x, words12: words, pcm, rate } : x,
        ));
        // Persist immediately so a cancelled refresh still keeps
        // whatever we managed to download. Next session re-attaches
        // from the cache without re-fetching, with the rate intact.
        try {
          await persistSampleToCache({ ...s, words12: words, pcm, rate });
        } catch (e) {
          console.warn(`refresh: cache write for slot ${s.slot} failed:`, e);
        }
      } catch (e) {
        console.warn(`refresh: audio for slot ${s.slot} failed:`, e);
      }
    }

    refreshState.set({
      ...INITIAL,
      phase: 'done',
      message: total === 0
        ? 'Refresh complete (no programs on device)'
        : `Refreshed ${total} program${total === 1 ? '' : 's'} + ${needsAudio.length} sample${needsAudio.length === 1 ? '' : 's'}`,
      progress: 100,
    });
  } catch (e: unknown) {
    const msg = e instanceof Error ? e.message : String(e);
    refreshState.set({
      ...INITIAL,
      phase: 'error',
      message: 'Refresh failed',
      error: msg,
    });
  }
}

function finishCancelled() {
  refreshState.set({
    ...INITIAL,
    phase: 'idle',
    message: 'Refresh cancelled',
  });
}
