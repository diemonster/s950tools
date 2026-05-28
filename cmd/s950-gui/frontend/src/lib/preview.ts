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
// The GainNode that sits between every active source and the
// destination. Owned per playback session so a fresh fade-in /
// fade-out curve can be scheduled without conflicting with leftover
// automation from a previous run.
let currentGain: GainNode | null = null;
// Secondary sources spawned alongside `current` (e.g. the ping-pong
// pre-roll node that bridges sample-start into the looping segment).
// Tracked separately so stop() can tear all of them down at once
// without losing the meaning of "is something playing" via `current`.
const extras: AudioBufferSourceNode[] = [];

// Sub-millisecond fade applied at play-START and play-STOP only.
// Suppresses the click you get when jumping from silence to the
// sample's first non-zero value, and the click when cutting out
// mid-loop. Deliberately NOT applied at loop seams — those need to
// ring through so the user can hear a mismatched loopStart/loopEnd
// (which is real diagnostic information they'd want to fix).
const FADE_SEC = 0.002;

// S950 loudness is ±50 in 0.375 dB steps (see protocol.program.go's
// SoftLoudness comment), so map directly to a linear gain factor.
// Positive values can exceed unity — the device clips there too, so
// matching that behaviour keeps preview faithful.
function loudnessToGain(loudness: number): number {
  return Math.pow(10, (loudness * 0.375) / 20);
}

// S950 tune is stored in 1/16-semitone units but the UI keeps it in
// semitones (with optional fractional cents). Web Audio's
// playbackRate scales both pitch and duration, which is exactly how
// the device's pitched playback works (no separate time-stretch).
function tuneToRate(tune: number): number {
  return Math.pow(2, tune / 12);
}

function makeFadeInGain(ac: AudioContext, target: number): GainNode {
  const gain = ac.createGain();
  const now = ac.currentTime;
  gain.gain.setValueAtTime(0, now);
  gain.gain.linearRampToValueAtTime(target, now + FADE_SEC);
  gain.connect(ac.destination);
  return gain;
}

// Schedules a linear ramp down to silence on the gain node, starting
// at `when` and ending FADE_SEC later. setValueAtTime pins the
// current gain value so the ramp begins from wherever automation
// happens to be — important if a previous ramp is mid-flight.
function scheduleFadeOutAt(gain: GainNode, ac: AudioContext, when: number) {
  const startVal = gain.gain.value;
  gain.gain.setValueAtTime(startVal, Math.max(when, ac.currentTime));
  gain.gain.linearRampToValueAtTime(0, Math.max(when, ac.currentTime) + FADE_SEC);
}

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

// buildReversedRegion synthesises a back-to-front copy of one sample
// range as a fresh AudioBuffer. Web Audio's playbackRate must be > 0,
// so reverse playback is implemented by reversing the PCM up front
// and playing the result forward. Built per preview call rather than
// cached — the range can change with each click (slice vs sample,
// edited Start/End) so a cache key would have to include them.
function buildReversedRegion(
  s: Sample,
  startWord: number,
  lengthWords: number,
  ac: AudioContext,
): AudioBuffer | null {
  if (!s.pcm || s.pcm.length === 0) return null;
  const total = s.pcm.length;
  const begin = Math.max(0, Math.min(total, startWord));
  const len   = Math.max(1, Math.min(lengthWords, total - begin));
  const buf = ac.createBuffer(1, len, s.rate);
  const ch  = buf.getChannelData(0);
  const pcm = s.pcm;
  for (let i = 0; i < len; i++) {
    ch[i] = pcm[begin + len - 1 - i] / 32768;
  }
  return buf;
}

// stop tears down the in-flight source(s). Calling preview again
// does this automatically — exposed so the spacebar handler can
// toggle playback (second press while audio is in flight should
// stop). Rather than cutting to silence (which clicks), we ramp the
// shared GainNode to 0 over FADE_SEC and schedule each source's
// stop() at the end of the ramp. The `current` / `currentGain` /
// `extras` refs clear synchronously so isPlaying() reports false
// immediately and a subsequent preview can start fresh.
export function stop() {
  if (currentGain) {
    const ac = audioContext();
    const fadeStart = ac.currentTime;
    const stopAt    = fadeStart + FADE_SEC;
    scheduleFadeOutAt(currentGain, ac, fadeStart);
    if (current) {
      try { current.stop(stopAt); } catch {}
    }
    for (const s of extras) {
      try { s.stop(stopAt); } catch {}
    }
    // Tidy up the graph once the fade has played out — disconnect on
    // each source's `ended` event, plus a setTimeout fallback for
    // the gain (no native end event). Leaking briefly is harmless
    // (GC takes them) but explicit disconnect keeps the audio graph
    // tidy across many preview clicks.
    const g = currentGain;
    if (current) {
      const c = current;
      c.addEventListener('ended', () => { try { c.disconnect(); } catch {} });
    }
    for (const s of extras) {
      s.addEventListener('ended', () => { try { s.disconnect(); } catch {} });
    }
    setTimeout(() => { try { g.disconnect(); } catch {} }, FADE_SEC * 1000 + 20);
  }
  current = null;
  currentGain = null;
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
  reverse = false,
): boolean {
  stop();
  const ac = audioContext();

  // For reverse playback, build a one-off reversed buffer over the
  // play region and treat the source coordinates as if start=0.
  // loopStartWord (a forward offset from the region start) flips to
  // count from the back of the (now-reversed) region. The rest of
  // the function uses these effective values uniformly so the
  // ping-pong / one-shot / loop branches don't need their own
  // reverse-aware paths.
  let buf: AudioBuffer | null;
  let effStartWord: number;
  let effLoopStartWord: number;
  if (reverse) {
    buf = buildReversedRegion(s, startWord, lengthWords, ac);
    effStartWord = 0;
    effLoopStartWord = Math.max(0, lengthWords - (loopStartWord + loopLengthWord));
  } else {
    buf = bufferFor(s);
    effStartWord = startWord;
    effLoopStartWord = loopStartWord;
  }
  if (!buf) return false;

  const startSec  = effStartWord  / s.rate;
  const lenSec    = lengthWords / s.rate;
  const loopStartAbs = startSec + effLoopStartWord / s.rate;
  const loopEndAbs   = startSec + (effLoopStartWord + loopLengthWord) / s.rate;

  const rate    = tuneToRate(s.tune ?? 0);
  const gainAmt = loudnessToGain(s.loudness ?? 0);

  // Build the per-session gain node now so every source we wire
  // below routes through it. Fade-in is scheduled inside the
  // factory; fade-out is scheduled below or in stop().
  const gain = makeFadeInGain(ac, gainAmt);

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
    preRoll.playbackRate.value = rate;
    preRoll.connect(gain);
    const preRollEnd = loopEndAbs;       // absolute seconds in source buffer
    const preRollDur = preRollEnd - startSec;
    preRoll.start(0, startSec, preRollDur);

    // Loop buffer: reverse half then forward half, both covering the
    // loop region. Looping this buffer produces alternating
    // backward/forward playback that bounces off loopStart and loopEnd.
    // See buildPingPongLoopBuffer() for the math.
    const srcArr = buf.getChannelData(0);
    const startIdx = Math.max(0, Math.floor(loopStartAbs * s.rate));
    const endIdx   = Math.min(srcArr.length, Math.floor(loopEndAbs * s.rate));
    const pingData = buildPingPongLoopBuffer(srcArr, startIdx, endIdx);
    const pingBuf  = ac.createBuffer(1, pingData.length, s.rate);
    pingBuf.getChannelData(0).set(pingData);

    const loopSrc = ac.createBufferSource();
    loopSrc.buffer = pingBuf;
    loopSrc.loop = true;
    loopSrc.playbackRate.value = rate;
    loopSrc.connect(gain);
    // playbackRate scales the pre-roll's real-time duration, so the
    // loop must start later/earlier than the source-buffer duration
    // would suggest. preRollDur is in source seconds; divide by rate
    // to get wall-clock seconds.
    loopSrc.start(ac.currentTime + preRollDur / rate);

    extras.push(preRoll);
    current = loopSrc;
    currentGain = gain;
    return true;
  }

  // ---------- One-shot + forward loop ----------
  const src = ac.createBufferSource();
  src.buffer = buf;
  src.playbackRate.value = rate;

  if (loopMode === 'loop') {
    src.loop = true;
    // loopStart/loopEnd are absolute seconds in the buffer (not
    // relative to the source's start parameter), per spec.
    src.loopStart = loopStartAbs;
    src.loopEnd   = loopEndAbs;
  }

  src.connect(gain);
  src.start(0, startSec, loopMode === 'one-shot' ? lenSec : undefined);

  // One-shot has a known end time — schedule the fade-out so the
  // tail of the clip ramps to silence instead of cutting at lenSec.
  // Clamp to half the clip length so very short samples don't
  // double-fade (in→out overlap = audible pop). For ≤2*FADE_SEC
  // clips, skip the out-fade entirely. lenSec is in source-buffer
  // seconds; divide by rate to convert to wall-clock seconds for
  // the AudioContext schedule.
  const realLenSec = lenSec / rate;
  if (loopMode === 'one-shot' && realLenSec > 2 * FADE_SEC) {
    const endTime = ac.currentTime + realLenSec;
    scheduleFadeOutAt(gain, ac, endTime - FADE_SEC);
  }

  current = src;
  currentGain = gain;
  // Auto-clear after the play window so subsequent stop() is a no-op.
  if (loopMode === 'one-shot') {
    src.onended = () => {
      if (current === src) { current = null; currentGain = null; }
      try { src.disconnect(); } catch {}
      try { gain.disconnect(); } catch {}
    };
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
  return previewRegion(s, s.start, len, loopMode, 0, s.loopLength, s.reverse);
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

// buildPingPongLoopBuffer constructs the audio segment that
// previewRegion loops to synthesise ping-pong playback. Web Audio
// has no native ping-pong, so we hand-stitch a [reverse, forward]
// buffer and play it with loop=true.
//
// Inputs: source samples and the loop region [startIdx, endIdx)
//   (endIdx exclusive). Pre-roll plays [..., endIdx-1] forward
//   before this buffer takes over.
//
// Output: a Float32Array of length 2*segLen where
//   • [0..segLen)        is the reverse traversal (endIdx-1 → startIdx)
//   • [segLen..2*segLen) is the forward traversal (startIdx → endIdx-1)
//
// Bounce points (reverse→forward inside the buffer, and end→start
// across the loop seam) repeat one sample each — at the natural
// zero-velocity turnaround that's perceptually fine. The shape is
// locked in by tests so we don't regress this on a casual refactor.
export function buildPingPongLoopBuffer(
  srcArr: Float32Array | ArrayLike<number>,
  startIdx: number,
  endIdx: number,
): Float32Array {
  const segLen = Math.max(1, endIdx - startIdx);
  const out = new Float32Array(segLen * 2);
  for (let i = 0; i < segLen; i++) out[i] = srcArr[endIdx - 1 - i];
  for (let i = 0; i < segLen; i++) out[segLen + i] = srcArr[startIdx + i];
  return out;
}
