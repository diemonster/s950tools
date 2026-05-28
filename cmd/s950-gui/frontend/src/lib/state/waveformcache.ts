// TS shim over the Go-side waveform cache (Phase 1B). Owns the
// session-to-session lifecycle of host-side audio for device-sourced
// samples: pulls cached words12 in after Connect's refreshCatalog
// drops in fresh device rows, and writes back after Apply Slicing
// uploads new content. PCM is derived on demand from words12 so the
// cache holds a single canonical format.
//
// The S950 is a 12-bit sampler in offset-binary; a "word" is a 12-bit
// unsigned with silence at 0x800. wordsToPcm converts to the
// signed-int16 scaling the rest of the GUI uses for PCM (matching
// what ImportSample produces from a .wav file), so the waveform
// path renderer's PCM_MAX=32768 divisor produces visible amplitudes
// instead of effectively-zero values. Pure helper — kept here so
// tests can hit it without importing the whole connection lifecycle.

import { get } from 'svelte/store';
import { samples, type Sample } from './samples';
import * as App from '../../../wailsjs/go/main/App';

// wordsToPcm converts a 12-bit offset-binary words12 buffer to the
// int16-scaled PCM the waveform renderer + Web Audio preview expect.
//
// The S950 stores audio as 12-bit unsigned with silence at 0x800.
// Subtracting 2048 maps that into signed 12-bit (-2048..+2047), and
// the `<< 4` then scales to signed 16-bit (-32768..+32752) — the
// same arithmetic the Go side's sample.SWtoPCM16 uses. Without the
// shift, every value would be 16x too small and the waveform path
// renderer (which divides by PCM_MAX = 32768) would draw a flat
// line at center even though the data is present and correct.
export function wordsToPcm(words: ReadonlyArray<number>): number[] {
  const out = new Array<number>(words.length);
  for (let i = 0; i < words.length; i++) {
    out[i] = ((words[i] & 0x0FFF) - 2048) << 4;
  }
  return out;
}

// hydrateSampleFromCache asks the Go cache for one sample's words12,
// returning the audio (words12 + derived pcm) when the cache hits,
// null when it misses. Wails fire-and-forget guarantees: errors are
// logged and treated as misses — a broken cache must never block the
// GUI from showing live device state.
export async function hydrateSampleFromCache(
  s: Sample,
): Promise<{ pcm: number[]; words12: number[] } | null> {
  try {
    const words = await (App as any).GetCachedWaveform(s.slot, s.name, s.length);
    if (!words || (Array.isArray(words) && words.length === 0)) return null;
    return { words12: words as number[], pcm: wordsToPcm(words) };
  } catch (e) {
    console.warn(`waveformcache: get(${s.slot}, ${s.name}) failed:`, e);
    return null;
  }
}

// hydrateAllSamplesFromCache walks every device-sourced row in the
// samples store and overlays cached audio onto the matches. Skips
// rows that already have pcm/words12 (Phase 1A's in-memory carry-over
// after Apply trumps a stale-on-disk entry). Concurrent fetches keep
// the post-Connect spin under control — Wails serialises IPC anyway
// but Promise.all lets the calls overlap with the Go-side file reads.
export async function hydrateAllSamplesFromCache(): Promise<void> {
  const list = get(samples);
  const candidates = list.filter(
    (s) => s.source === 'device' && (!s.words12 || s.words12.length === 0),
  );
  if (candidates.length === 0) return;

  const fetched = await Promise.all(
    candidates.map(async (s) => ({ slot: s.slot, audio: await hydrateSampleFromCache(s) })),
  );

  // Build a slot → audio map of hits only, then overlay. Skipping the
  // misses keeps unrelated rows untouched and avoids an unnecessary
  // store write when nothing was found.
  const hits = new Map<number, { pcm: number[]; words12: number[] }>();
  for (const f of fetched) {
    if (f.audio) hits.set(f.slot, f.audio);
  }
  if (hits.size === 0) return;

  samples.update((xs) =>
    xs.map((s) => {
      const cap = hits.get(s.slot);
      if (!cap) return s;
      return { ...s, pcm: cap.pcm, words12: cap.words12 };
    }),
  );
}

// persistSampleToCache writes a sample's host audio to the on-disk
// cache. Called from the Apply Slicing success path so the next
// session can re-attach without a re-upload. No-op when the sample
// has no host audio (nothing to cache).
//
// IMPORTANT: the cache key uses `words12.length`, not `s.length`.
// At Apply-success time scanMemory hasn't refreshed the SPRM yet so
// `s.length` is still the skinny default (1) — pairing that key
// with the real multi-thousand-word body would write a header/key
// mismatch the Go reader (correctly) rejects as ErrCorrupt on next
// session. Using the buffer's actual length means the cache key
// always describes what's in the file.
export async function persistSampleToCache(s: Sample): Promise<void> {
  if (!s.words12 || s.words12.length === 0) return;
  try {
    await (App as any).PutCachedWaveform(s.slot, s.name, s.words12.length, s.words12);
  } catch (e) {
    // Persistence is best-effort: a write failure degrades the next
    // session back to the synthetic waveform, but the live session
    // is unaffected. Log and move on.
    console.warn(`waveformcache: put(${s.slot}, ${s.name}) failed:`, e);
  }
}

// persistSamplesToCache fires off persistSampleToCache for each
// captured slot in parallel. Used right after Apply Slicing attaches
// audio in memory — the disk write is fire-and-forget from the
// caller's perspective.
export async function persistSamplesToCache(slots: ReadonlyArray<number>): Promise<void> {
  const list = get(samples);
  const targets = list.filter((s) => slots.includes(s.slot));
  await Promise.all(targets.map(persistSampleToCache));
}
