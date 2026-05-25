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
// Secondary sources spawned alongside `current` (e.g. the ping-pong
// pre-roll node that bridges sample-start into the looping segment).
// Tracked separately so stop() can tear all of them down at once
// without losing the meaning of "is something playing" via `current`.
const extras: AudioBufferSourceNode[] = [];

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
// this automatically — exposed so the spacebar handler can toggle
// playback (second press while audio is in flight should stop).
export function stop() {
  if (current) {
    try { current.stop(); } catch {}
    current.disconnect();
    current = null;
  }
  // Tear down any secondary sources we scheduled (e.g. the ping-pong
  // pre-roll feeding into the loop source).
  for (const s of extras) {
    try { s.stop(); } catch {}
    s.disconnect();
  }
  extras.length = 0;
}

// isPlaying lets the UI toggle preview on/off with one shortcut:
// pressing space again while a loop is running should stop, not
// stack a second source on top.
export function isPlaying(): boolean {
  return current !== null;
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

  const startSec  = startWord  / s.rate;
  const lenSec    = lengthWords / s.rate;
  const loopStartAbs = startSec + loopStartWord / s.rate;
  const loopEndAbs   = startSec + (loopStartWord + loopLengthWord) / s.rate;

  // ---------- Ping-pong ----------
  // Web Audio's native loop is forward-only. We synthesise ping-pong
  // by building a stitched buffer of [reverse(loop), forward(loop)]
  // and looping that. The reverse → forward boundary is mathematically
  // continuous (both halves meet at the loopStart sample), so the
  // loop seam is inaudible.
  if (loopMode === 'ping-pong' && loopLengthWord > 0) {
    // Pre-roll: play normally from startWord up to the end of the
    // loop region (the standard ping-pong entry), no loop.
    const preRoll = ac.createBufferSource();
    preRoll.buffer = buf;
    preRoll.connect(ac.destination);
    const preRollEnd = loopEndAbs;       // absolute seconds in source buffer
    const preRollDur = preRollEnd - startSec;
    preRoll.start(0, startSec, preRollDur);

    // Loop buffer: reverse half then forward half, both covering the
    // loop region. Looping this buffer produces alternating
    // backward/forward playback that bounces off loopStart and loopEnd.
    const srcArr = buf.getChannelData(0);
    const startIdx = Math.max(0, Math.floor(loopStartAbs * s.rate));
    const endIdx   = Math.min(srcArr.length, Math.floor(loopEndAbs * s.rate));
    const segLen   = Math.max(1, endIdx - startIdx);
    const pingBuf  = ac.createBuffer(1, segLen * 2, s.rate);
    const dst = pingBuf.getChannelData(0);
    for (let i = 0; i < segLen; i++) dst[i] = srcArr[endIdx - 1 - i];     // reverse
    for (let i = 0; i < segLen; i++) dst[segLen + i] = srcArr[startIdx + i]; // forward

    const loopSrc = ac.createBufferSource();
    loopSrc.buffer = pingBuf;
    loopSrc.loop = true;
    loopSrc.connect(ac.destination);
    loopSrc.start(ac.currentTime + preRollDur);

    extras.push(preRoll);
    current = loopSrc;
    return true;
  }

  // ---------- One-shot + forward loop ----------
  const src = ac.createBufferSource();
  src.buffer = buf;

  if (loopMode === 'loop') {
    src.loop = true;
    // loopStart/loopEnd are absolute seconds in the buffer (not
    // relative to the source's start parameter), per spec.
    src.loopStart = loopStartAbs;
    src.loopEnd   = loopEndAbs;
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
