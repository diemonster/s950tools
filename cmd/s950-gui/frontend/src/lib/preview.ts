// Web Audio preview — host-side playback of imported PCM. Never
// touches the device or MIDI; entirely safe to use freely while
// editing. Driven by the ▶ buttons in the Slices list, the
// (future) ▶ on the Sample identity strip, and spacebar.
//
// One shared AudioContext, one in-flight source at a time. Hitting
// preview while audio is playing stops the previous source first —
// matches the typical "audition" pattern.

import type { Sample } from './state/samples';
import type { Slice } from './state/slicing';

// AudioContext is lazily-created — most browsers gate it on user
// gesture, and we want preview to "just work" when the user clicks ▶
// even if they never explicitly resumed the context.
let ctx: AudioContext | null = null;
let current: AudioBufferSourceNode | null = null;

function audioContext(): AudioContext {
  if (!ctx) {
    ctx = new (window.AudioContext || (window as any).webkitAudioContext)();
  }
  if (ctx.state === 'suspended') {
    void ctx.resume();
  }
  return ctx;
}

// Cache decoded AudioBuffers per sample slot so we don't re-allocate
// + re-fill a Float32Array every time the user clicks ▶ on a slice.
// Invalidated when the sample's PCM changes (different identity).
const bufferCache = new Map<number, { pcm: number[]; rate: number; buf: AudioBuffer }>();

function bufferFor(s: Sample): AudioBuffer | null {
  if (!s.pcm || s.pcm.length === 0) return null;
  const cached = bufferCache.get(s.slot);
  // Compare by identity (Wails returns a new array each fetch); ===
  // catches the "user re-imported" case so we rebuild the buffer.
  if (cached && cached.pcm === s.pcm && cached.rate === s.rate) {
    return cached.buf;
  }
  const ac = audioContext();
  const buf = ac.createBuffer(1, s.pcm.length, s.rate);
  const ch = buf.getChannelData(0);
  const pcm = s.pcm;
  for (let i = 0; i < pcm.length; i++) {
    ch[i] = pcm[i] / 32768;
  }
  bufferCache.set(s.slot, { pcm, rate: s.rate, buf });
  return buf;
}

// stop tears down the in-flight source. Calling preview again does
// this automatically — exposed so a "press Esc to stop" shortcut
// can wire up later without going through play().
export function stop() {
  if (current) {
    try { current.stop(); } catch {}
    current.disconnect();
    current = null;
  }
}

// previewRegion plays [startWord..startWord+lengthWords] of the
// sample's PCM through the user's speakers. If loop info is given
// it's applied via Web Audio's native loop fields; ping-pong isn't
// natively supported so it falls back to forward-loop with a
// console note.
//
// Returns true if playback started, false if there's no host-side
// audio (sample wasn't imported, or the .pcm field is empty).
export function previewRegion(
  s: Sample,
  startWord: number,
  lengthWords: number,
  loopMode: 'one-shot' | 'loop' | 'ping-pong' = 'one-shot',
  loopStartWord = 0,
  loopLengthWord = 0,
): boolean {
  const buf = bufferFor(s);
  if (!buf) return false;
  stop();
  const ac = audioContext();
  const src = ac.createBufferSource();
  src.buffer = buf;

  const startSec  = startWord  / s.rate;
  const lenSec    = lengthWords / s.rate;
  const endSec    = startSec + lenSec;

  if (loopMode === 'loop' || loopMode === 'ping-pong') {
    if (loopMode === 'ping-pong') {
      // TODO: Web Audio has no native ping-pong. Could emulate with
      // a second source playing in reverse with a delay equal to one
      // loop length, but it's a meaningful chunk of work. Falls back
      // to forward-loop for now — still useful for clickless-loop
      // auditioning.
      console.info('[preview] ping-pong → forward-loop (Web Audio fallback)');
    }
    src.loop = true;
    // loopStart/loopEnd are absolute seconds in the buffer (not
    // relative to the source's start parameter), per spec.
    src.loopStart = startSec + loopStartWord / s.rate;
    src.loopEnd   = startSec + (loopStartWord + loopLengthWord) / s.rate;
  }

  src.connect(ac.destination);
  src.start(0, startSec, loopMode === 'one-shot' ? lenSec : undefined);
  current = src;
  // Auto-clear after the play window so subsequent stop() is a no-op.
  if (loopMode === 'one-shot') {
    src.onended = () => { if (current === src) current = null; };
  }
  return true;
}

// previewSample plays the active region of a sample (Start..End)
// respecting its replay mode + reverse flag. Returns false when no
// host-side audio is available.
export function previewSample(s: Sample): boolean {
  if (!s.pcm) return false;
  const len = Math.max(1, s.end - s.start);
  const loopMode = s.mode;
  // Loop relative to slice start = (Start..LoopLength) since the S950
  // anchors loops at the sample's start point.
  return previewRegion(s, s.start, len, loopMode, 0, s.loopLength);
}

// previewSlice plays one slice of a sample (which is itself part of
// the imported source). The slice's loop config is applied within
// the slice's local window.
export function previewSlice(s: Sample, sl: Slice): boolean {
  if (!s.pcm) return false;
  return previewRegion(s, sl.start, sl.length, sl.loopMode, sl.loopStart, sl.loopLength);
}

// hasHostAudio is the UI guard for the ▶ button: dim it when the
// sample has no in-memory PCM (i.e. wasn't imported in this session).
export function hasHostAudio(s: Sample | undefined): boolean {
  return Boolean(s && s.pcm && s.pcm.length > 0);
}
