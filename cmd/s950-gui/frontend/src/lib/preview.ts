// Web Audio preview — host-side playback of imported PCM. Never
// touches the device or MIDI; entirely safe to use freely while
// editing. Driven by the ▶ buttons in the Slices list, the
// (future) ▶ on the Sample identity strip, and spacebar.
//
// One shared AudioContext, one in-flight source at a time. Hitting
// preview while audio is playing stops the previous source first —
// matches the typical "audition" pattern.

import { writable } from 'svelte/store';
import type { Sample } from './state/samples';
import type { Slice } from './state/slicing';

// playhead is a public store the Sample-tab waveform subscribes to
// for the in-flight playback cursor. While playback is live it
// updates ~60Hz with the current source-buffer word index; on stop
// it flips back to inactive. Components that don't need the cursor
// can ignore it — store is cheap (single object set per rAF tick).
export type Playhead = {
  active: boolean;
  // Sample slot the cursor belongs to. Lets the Sample tab ignore
  // playhead updates that aren't for the row currently on screen.
  sampleSlot: number;
  // Current word index in the source PCM. NOT in slice-local coords
  // — the waveform's marker math is also in source-word space.
  currentWord: number;
};
export const playhead = writable<Playhead>({
  active: false,
  sampleSlot: -1,
  currentWord: 0,
});

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

// playbackSession is the snapshot previewRegion records when it
// starts a source. The rAF tracker reads it to map elapsed
// wall-clock time → current word index, accounting for the loop
// mode and direction. Cleared in stop().
type PlaybackSession = {
  sampleSlot: number;
  startWord: number;
  lengthWords: number;
  loopMode: 'one-shot' | 'loop' | 'ping-pong';
  loopStartWord: number;
  loopLengthWord: number;
  reverse: boolean;
  rate: number;            // playbackRate (tune-derived)
  sampleRateHz: number;    // source AudioBuffer sample rate
  startedAt: number;       // AudioContext.currentTime at source.start
};
let session: PlaybackSession | null = null;
let playheadRaf: number | null = null;

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

// zohRenderPCM renders source-rate int16 PCM into a destination-rate
// Float32 buffer using nearest-neighbor (zero-order-hold) when
// upsampling. The S950 has no anti-aliasing filter on playback —
// it just holds each sample's value until the next clock tick —
// so the host preview needs to do the same to sound like the
// device. Without this, Web Audio would sinc-interpolate the
// source PCM into the AudioContext rate and we'd lose the
// characteristic stair-step / aliasing of low-rate samples.
//
// When fromRate >= toRate we let the browser's downsampler handle
// it (the inverse case — S950 samples up to 65 kHz on a 48 kHz
// AudioContext is rare and not the lo-fi-character path).
export function zohRenderPCM(pcm: number[], fromRate: number, toRate: number, out: Float32Array): void {
  if (pcm.length === 0) return; // empty input → silent buffer (already zeroed)
  if (fromRate >= toRate) {
    // Pass-through 1:1 (or browser handles the downsample on play).
    const n = Math.min(out.length, pcm.length);
    for (let i = 0; i < n; i++) out[i] = pcm[i] / 32768;
    return;
  }
  // Nearest-neighbor upsample. Each output frame indexes back into
  // the source via floor(i * fromRate / toRate). Multiple consecutive
  // outputs landing on the same source sample = the held value,
  // which is exactly what the S950's DAC does. Clamp the index to
  // the last source frame — Math.floor rounding plus the dstFrames
  // ceil() in the caller can push srcIdx one past the end on the
  // very last output frame, which would otherwise read `undefined`
  // and produce NaN in the AudioBuffer (audible as clicks).
  const lastSrc = pcm.length - 1;
  for (let i = 0; i < out.length; i++) {
    let srcIdx = Math.floor((i * fromRate) / toRate);
    if (srcIdx > lastSrc) srcIdx = lastSrc;
    out[i] = pcm[srcIdx] / 32768;
  }
}

function bufferFor(s: Sample): AudioBuffer | null {
  if (!s.pcm || s.pcm.length === 0) return null;
  const cached = bufferCache.get(s.slot);
  // Compare by identity (Wails returns a new array each fetch); ===
  // catches the "user re-imported" case so we rebuild the buffer.
  if (cached && cached.pcm === s.pcm && cached.rate === s.rate) {
    return cached.buf;
  }
  const ac = audioContext();
  // Render at the AudioContext's native rate so the browser doesn't
  // re-interpolate a sub-rate buffer with its (high-quality) built-in
  // resampler — that would smooth away the lo-fi character the
  // resample-on-upload feature was meant to add. ZOH expansion
  // (sample-and-hold) keeps the device's stair-step audible. For
  // same-or-higher source rates this is a 1:1 copy.
  const dstRate = ac.sampleRate;
  const dstFrames = s.rate < dstRate
    ? Math.ceil((s.pcm.length * dstRate) / s.rate)
    : s.pcm.length;
  const buf = ac.createBuffer(1, dstFrames, dstRate);
  zohRenderPCM(s.pcm, s.rate, dstRate, buf.getChannelData(0));
  bufferCache.set(s.slot, { pcm: s.pcm, rate: s.rate, buf });
  return buf;
}

// buildReversedRegion synthesises a back-to-front copy of one sample
// range as a fresh AudioBuffer. Web Audio's playbackRate must be > 0,
// so reverse playback is implemented by reversing the PCM up front
// and playing the result forward. Built per preview call rather than
// cached — the range can change with each click (slice vs sample,
// edited Start/End) so a cache key would have to include them.
//
// Like bufferFor, we render at the AudioContext's native rate via
// zero-order-hold expansion so a low-rate (lo-fi) sample doesn't
// get smoothed by the browser's built-in resampler on playback.
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
  // Reverse first in source space — a contiguous int16 slice — then
  // ZOH-expand to destination rate. Order doesn't matter mathematically
  // (the two operations commute on nearest-neighbor expansion), and
  // building the reverse buffer first keeps the helper one-shot.
  const reversed: number[] = new Array(len);
  for (let i = 0; i < len; i++) {
    reversed[i] = s.pcm[begin + len - 1 - i];
  }
  const dstRate = ac.sampleRate;
  const dstFrames = s.rate < dstRate
    ? Math.ceil((len * dstRate) / s.rate)
    : len;
  const buf = ac.createBuffer(1, dstFrames, dstRate);
  zohRenderPCM(reversed, s.rate, dstRate, buf.getChannelData(0));
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
  stopPlayheadTracker();
}

// isPlaying lets the UI toggle preview on/off with one shortcut:
// pressing space again while a loop is running should stop, not
// stack a second source on top.
export function isPlaying(): boolean {
  return current !== null;
}

// currentWordFor maps elapsed wall-clock time to a position in the
// source PCM, honouring the session's loop mode + direction. The
// playhead UI cursor reads this every animation frame; accuracy
// matters most at loop boundaries (a slow tick at the seam looks
// like the cursor "snags"). Returned word is clamped to the
// playback range — callers can render it directly.
function currentWordFor(s: PlaybackSession, elapsedSec: number): number {
  const wordsAdvanced = elapsedSec * s.rate * s.sampleRateHz;

  if (s.reverse) {
    // Reverse playback synthesises a reversed buffer of the play
    // range and plays it forward — so in source-word space, the
    // position decreases from end as time advances. Loop modes
    // with reverse are an unusual combo on the device; we just
    // clamp at startWord rather than try to oscillate.
    const endWord = s.startWord + s.lengthWords;
    return Math.max(s.startWord, endWord - wordsAdvanced);
  }

  switch (s.loopMode) {
    case 'one-shot': {
      const pos = s.startWord + wordsAdvanced;
      return Math.min(s.startWord + s.lengthWords, pos);
    }
    case 'loop': {
      // Native Web Audio loop: source plays from startWord, then
      // wraps to loopStart when it crosses loopEnd. Mirror that
      // here so the cursor matches what the user hears.
      if (s.loopLengthWord <= 0) {
        return Math.min(s.startWord + s.lengthWords, s.startWord + wordsAdvanced);
      }
      const loopStart = s.startWord + s.loopStartWord;
      const loopEnd = loopStart + s.loopLengthWord;
      const pos = s.startWord + wordsAdvanced;
      if (pos <= loopEnd) return pos;
      const overflow = (pos - loopEnd) % s.loopLengthWord;
      return loopStart + overflow;
    }
    case 'ping-pong': {
      // previewRegion's ping-pong is pre-roll (startWord → loopEnd)
      // then a stitched [reverse(loop), forward(loop)] buffer on
      // loop. The visual position oscillates inside the loop region
      // after the pre-roll: triangle wave with period 2*loopLength.
      const loopStart = s.startWord + s.loopStartWord;
      const loopEnd = loopStart + s.loopLengthWord;
      const preRollWords = loopEnd - s.startWord;
      if (s.loopLengthWord <= 0 || wordsAdvanced <= preRollWords) {
        return Math.min(loopEnd, s.startWord + wordsAdvanced);
      }
      const inLoop = wordsAdvanced - preRollWords;
      const period = 2 * s.loopLengthWord;
      const phase = inLoop % period;
      if (phase < s.loopLengthWord) {
        // Reverse half: loopEnd → loopStart
        return loopEnd - phase;
      }
      // Forward half: loopStart → loopEnd
      return loopStart + (phase - s.loopLengthWord);
    }
  }
}

// startPlayheadTracker records the new session and kicks off the
// rAF loop that drives the `playhead` store. Replaces any prior
// session — only one preview can be in flight at a time so the
// loop is single-shot.
function startPlayheadTracker(s: PlaybackSession) {
  session = s;
  if (playheadRaf !== null) cancelAnimationFrame(playheadRaf);
  const tick = () => {
    if (!session) return;
    const elapsed = audioContext().currentTime - session.startedAt;
    const word = currentWordFor(session, Math.max(0, elapsed));
    playhead.set({
      active: true,
      sampleSlot: session.sampleSlot,
      currentWord: word,
    });
    playheadRaf = requestAnimationFrame(tick);
  };
  playheadRaf = requestAnimationFrame(tick);
}

function stopPlayheadTracker() {
  if (playheadRaf !== null) cancelAnimationFrame(playheadRaf);
  playheadRaf = null;
  session = null;
  playhead.set({ active: false, sampleSlot: -1, currentWord: 0 });
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
    startPlayheadTracker({
      sampleSlot: s.slot,
      startWord, lengthWords, loopMode,
      loopStartWord, loopLengthWord,
      reverse,
      rate,
      sampleRateHz: s.rate,
      startedAt: ac.currentTime,
    });
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
  startPlayheadTracker({
    sampleSlot: s.slot,
    startWord, lengthWords, loopMode,
    loopStartWord, loopLengthWord,
    reverse,
    rate,
    sampleRateHz: s.rate,
    startedAt: ac.currentTime,
  });
  // Auto-clear after the play window so subsequent stop() is a no-op.
  if (loopMode === 'one-shot') {
    src.onended = () => {
      if (current === src) { current = null; currentGain = null; }
      try { src.disconnect(); } catch {}
      try { gain.disconnect(); } catch {}
      // The rAF loop would otherwise keep ticking until the next
      // user action; clear the playhead at the natural end of a
      // one-shot too.
      stopPlayheadTracker();
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
